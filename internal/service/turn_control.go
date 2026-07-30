package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/turn/v4"
)

const minTURNCredentialTTL = time.Minute
const maxTURNCredentialTTL = 24 * time.Hour
const minTURNEgressWindow = time.Minute
const maxTURNEgressWindow = 24 * time.Hour

type turnAllocationStats struct {
	Active           int
	Users            int
	IPs              int
	EgressBytes      int64
	DroppedBytes     int64
	QuotaExceeded    int64
	TrafficUsers     int
	TrafficIPs       int
	MaxEgressBytes   int64
	MaxEgressPerUser int64
	MaxEgressPerIP   int64
	EgressWindow     time.Duration
}

type turnAllocationQuota struct {
	maxTotal            int
	maxPerUser          int
	maxPerIP            int
	total               int
	byUser              map[string]int
	byIP                map[string]int
	allocationsByClient map[string]turnAllocationIdentity
	allocationsByRelay  map[string]turnAllocationIdentity
	egressWindow        time.Duration
	maxEgressBytes      int64
	maxEgressPerUser    int64
	maxEgressPerIP      int64
	globalBucket        turnByteBucket
	userBuckets         map[string]*turnByteBucket
	ipBuckets           map[string]*turnByteBucket
	egressBytes         int64
	droppedBytes        int64
	quotaExceeded       int64
	lastBucketCleanup   time.Time
	now                 func() time.Time
	mu                  sync.Mutex
}

type turnAllocationIdentity struct {
	User      string
	IP        string
	ClientKey string
	RelayKey  string
}

type turnByteBucket struct {
	Tokens float64
	Last   time.Time
}

type turnAccountingPacketConn struct {
	net.PacketConn
	quota       *turnAllocationQuota
	lookupRelay bool
	key         string
}

type turnAccountingRelayAddressGenerator struct {
	base  turn.RelayAddressGenerator
	quota *turnAllocationQuota
}

func (s *Server) validateConfig() error {
	if err := s.validateHTTPConfig(); err != nil {
		return err
	}
	if s.cfg.TURNCredentialTTL < minTURNCredentialTTL || s.cfg.TURNCredentialTTL > maxTURNCredentialTTL {
		return fmt.Errorf("TURN credential TTL must be between %s and %s", minTURNCredentialTTL, maxTURNCredentialTTL)
	}
	if s.cfg.TURNCredentialsPerMinute < -1 {
		return errors.New("TURN credential rate must be -1 or greater")
	}
	for name, value := range map[string]int{
		"TURN max allocations":          s.cfg.TURNMaxAllocations,
		"TURN max allocations per user": s.cfg.TURNMaxAllocationsPerUser,
		"TURN max allocations per IP":   s.cfg.TURNMaxAllocationsPerIP,
	} {
		if value < -1 {
			return fmt.Errorf("%s must be -1 or greater", name)
		}
	}
	if s.cfg.TURNEgressWindow < minTURNEgressWindow || s.cfg.TURNEgressWindow > maxTURNEgressWindow {
		return fmt.Errorf("TURN egress window must be between %s and %s", minTURNEgressWindow, maxTURNEgressWindow)
	}
	for name, value := range map[string]int64{
		"TURN max egress bytes":          s.cfg.TURNMaxEgressBytes,
		"TURN max egress bytes per user": s.cfg.TURNMaxEgressBytesPerUser,
		"TURN max egress bytes per IP":   s.cfg.TURNMaxEgressBytesPerIP,
	} {
		if value < -1 || value == 0 {
			return fmt.Errorf("%s must be -1 or positive", name)
		}
	}
	if s.cfg.TURNListen == "" &&
		(s.cfg.TURNMaxEgressBytes >= 0 ||
			s.cfg.TURNMaxEgressBytesPerUser >= 0 ||
			s.cfg.TURNMaxEgressBytesPerIP >= 0) {
		return errors.New("TURN egress quotas require --turn-listen")
	}
	if s.cfg.TURNSecret != "" && s.cfg.TURN == "" && s.cfg.TURNListen == "" {
		return errors.New("TURN shared secret requires --turn or --turn-listen")
	}
	if (s.cfg.TURNMinPort == 0) != (s.cfg.TURNMaxPort == 0) {
		return errors.New("TURN relay minimum and maximum ports must be configured together")
	}
	if s.cfg.TURNMinPort < 0 || s.cfg.TURNMinPort > 65534 ||
		s.cfg.TURNMaxPort < 0 || s.cfg.TURNMaxPort > 65534 ||
		s.cfg.TURNMinPort > s.cfg.TURNMaxPort {
		return errors.New("TURN relay port range must be between 1 and 65534 with minimum <= maximum")
	}
	if s.cfg.TURNMinPort != 0 && s.cfg.TURNListen == "" {
		return errors.New("TURN relay port range requires --turn-listen")
	}
	if s.cfg.TURNPublicIP != "" {
		ip := net.ParseIP(s.cfg.TURNPublicIP)
		if ip == nil || ip.To4() == nil {
			return errors.New("TURN public IP must be an IPv4 address")
		}
	}
	return nil
}

func (s *Server) turnCredentials(now time.Time) (username string, credential string, expiresAt int64, err error) {
	if s.cfg.TURNSecret == "" {
		return s.cfg.TURNUsername, s.cfg.TURNCredential, 0, nil
	}
	randomID := make([]byte, 8)
	if _, err := rand.Read(randomID); err != nil {
		return "", "", 0, err
	}
	expires := now.Add(s.cfg.TURNCredentialTTL).Unix()
	username = strconv.FormatInt(expires, 10) + ":" + s.cfg.TURNUsername + "-" + hex.EncodeToString(randomID)
	mac := hmac.New(sha1.New, []byte(s.cfg.TURNSecret))
	if _, err := mac.Write([]byte(username)); err != nil {
		return "", "", 0, err
	}
	credential = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return username, credential, expires * 1000, nil
}

func (s *Server) turnCredentialMode() string {
	switch {
	case s.cfg.TURN == "":
		return "none"
	case s.cfg.TURNSecret != "":
		return "temporary"
	default:
		return "static"
	}
}

func (s *Server) turnStats() turnAllocationStats {
	if s.turnQuota == nil {
		return turnAllocationStats{}
	}
	return s.turnQuota.Stats()
}

func newTURNAllocationQuota(maxTotal, maxPerUser, maxPerIP int) *turnAllocationQuota {
	return &turnAllocationQuota{
		maxTotal:            maxTotal,
		maxPerUser:          maxPerUser,
		maxPerIP:            maxPerIP,
		byUser:              map[string]int{},
		byIP:                map[string]int{},
		allocationsByClient: map[string]turnAllocationIdentity{},
		allocationsByRelay:  map[string]turnAllocationIdentity{},
		maxEgressBytes:      -1,
		maxEgressPerUser:    -1,
		maxEgressPerIP:      -1,
		userBuckets:         map[string]*turnByteBucket{},
		ipBuckets:           map[string]*turnByteBucket{},
		now:                 time.Now,
	}
}

func (q *turnAllocationQuota) ConfigureTraffic(window time.Duration, maxTotal, maxPerUser, maxPerIP int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.egressWindow = window
	q.maxEgressBytes = maxTotal
	q.maxEgressPerUser = maxPerUser
	q.maxEgressPerIP = maxPerIP
}

func (q *turnAllocationQuota) Allow(username, _ string, srcAddr net.Addr) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	host := addressHost(srcAddr)
	return belowQuota(q.total, q.maxTotal) &&
		belowQuota(q.byUser[username], q.maxPerUser) &&
		belowQuota(q.byIP[host], q.maxPerIP)
}

func (q *turnAllocationQuota) Created(srcAddr, _ net.Addr, _, username, _ string, relayAddr net.Addr, _ int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	clientKey := addressKey(srcAddr)
	relayKey := addressKey(relayAddr)
	identity := turnAllocationIdentity{
		User:      username,
		IP:        addressHost(srcAddr),
		ClientKey: clientKey,
		RelayKey:  relayKey,
	}
	q.total++
	q.byUser[username]++
	q.byIP[identity.IP]++
	if clientKey != "" {
		q.allocationsByClient[clientKey] = identity
	}
	if relayKey != "" {
		q.allocationsByRelay[relayKey] = identity
	}
}

func (q *turnAllocationQuota) Deleted(srcAddr, _ net.Addr, _, username, _ string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	clientKey := addressKey(srcAddr)
	identity, ok := q.allocationsByClient[clientKey]
	if ok {
		delete(q.allocationsByClient, clientKey)
		delete(q.allocationsByRelay, identity.RelayKey)
	}
	q.total = max(q.total-1, 0)
	decrementCounter(q.byUser, username)
	decrementCounter(q.byIP, addressHost(srcAddr))
}

func (q *turnAllocationQuota) Stats() turnAllocationStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return turnAllocationStats{
		Active:           q.total,
		Users:            len(q.byUser),
		IPs:              len(q.byIP),
		EgressBytes:      q.egressBytes,
		DroppedBytes:     q.droppedBytes,
		QuotaExceeded:    q.quotaExceeded,
		TrafficUsers:     len(q.userBuckets),
		TrafficIPs:       len(q.ipBuckets),
		MaxEgressBytes:   q.maxEgressBytes,
		MaxEgressPerUser: q.maxEgressPerUser,
		MaxEgressPerIP:   q.maxEgressPerIP,
		EgressWindow:     q.egressWindow,
	}
}

func (q *turnAllocationQuota) ConsumeClient(addr net.Addr, bytes int) bool {
	return q.consume(q.allocationsByClient, addressKey(addr), bytes)
}

func (q *turnAllocationQuota) ConsumeRelay(key string, bytes int) bool {
	return q.consume(q.allocationsByRelay, key, bytes)
}

func (q *turnAllocationQuota) consume(
	allocations map[string]turnAllocationIdentity,
	key string,
	bytes int,
) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	identity, tracked := allocations[key]
	if !tracked || bytes <= 0 {
		return true
	}
	now := q.now()
	q.cleanupBucketsLocked(now)

	buckets := make([]turnBucketLimit, 0, 3)
	if q.maxEgressBytes >= 0 {
		buckets = append(buckets, turnBucketLimit{Bucket: &q.globalBucket, Limit: q.maxEgressBytes})
	}
	if q.maxEgressPerUser >= 0 {
		buckets = append(buckets, turnBucketLimit{
			Bucket: bucketFor(q.userBuckets, identity.User),
			Limit:  q.maxEgressPerUser,
		})
	}
	if q.maxEgressPerIP >= 0 {
		buckets = append(buckets, turnBucketLimit{
			Bucket: bucketFor(q.ipBuckets, identity.IP),
			Limit:  q.maxEgressPerIP,
		})
	}
	for _, candidate := range buckets {
		refillBucket(candidate.Bucket, candidate.Limit, q.egressWindow, now)
		if candidate.Bucket.Tokens < float64(bytes) {
			q.droppedBytes += int64(bytes)
			q.quotaExceeded++
			return false
		}
	}
	for _, candidate := range buckets {
		candidate.Bucket.Tokens -= float64(bytes)
	}
	q.egressBytes += int64(bytes)
	return true
}

type turnBucketLimit struct {
	Bucket *turnByteBucket
	Limit  int64
}

func bucketFor(buckets map[string]*turnByteBucket, key string) *turnByteBucket {
	bucket := buckets[key]
	if bucket == nil {
		bucket = &turnByteBucket{}
		buckets[key] = bucket
	}
	return bucket
}

func refillBucket(bucket *turnByteBucket, limit int64, window time.Duration, now time.Time) {
	if bucket.Last.IsZero() {
		bucket.Tokens = float64(limit)
		bucket.Last = now
		return
	}
	if elapsed := now.Sub(bucket.Last); elapsed > 0 {
		bucket.Tokens = min(
			float64(limit),
			bucket.Tokens+elapsed.Seconds()*float64(limit)/window.Seconds(),
		)
		bucket.Last = now
	}
}

func (q *turnAllocationQuota) cleanupBucketsLocked(now time.Time) {
	if q.egressWindow <= 0 || now.Sub(q.lastBucketCleanup) < time.Minute {
		return
	}
	q.lastBucketCleanup = now
	for user, bucket := range q.userBuckets {
		if q.byUser[user] == 0 && now.Sub(bucket.Last) >= q.egressWindow {
			delete(q.userBuckets, user)
		}
	}
	for ip, bucket := range q.ipBuckets {
		if q.byIP[ip] == 0 && now.Sub(bucket.Last) >= q.egressWindow {
			delete(q.ipBuckets, ip)
		}
	}
}

func (c *turnAccountingPacketConn) WriteTo(payload []byte, addr net.Addr) (int, error) {
	allowed := false
	if c.lookupRelay {
		allowed = c.quota.ConsumeRelay(c.key, len(payload))
	} else {
		allowed = c.quota.ConsumeClient(addr, len(payload))
	}
	if !allowed {
		return len(payload), nil
	}
	return c.PacketConn.WriteTo(payload, addr)
}

func (g *turnAccountingRelayAddressGenerator) Validate() error {
	return g.base.Validate()
}

func (g *turnAccountingRelayAddressGenerator) AllocatePacketConn(
	network string,
	requestedPort int,
) (net.PacketConn, net.Addr, error) {
	conn, relayAddr, err := g.base.AllocatePacketConn(network, requestedPort)
	if err != nil {
		return nil, nil, err
	}
	return &turnAccountingPacketConn{
		PacketConn:  conn,
		quota:       g.quota,
		lookupRelay: true,
		key:         addressKey(relayAddr),
	}, relayAddr, nil
}

func (g *turnAccountingRelayAddressGenerator) AllocateConn(
	network string,
	requestedPort int,
) (net.Conn, net.Addr, error) {
	return g.base.AllocateConn(network, requestedPort)
}

func belowQuota(current, maximum int) bool {
	return maximum < 0 || current < maximum
}

func decrementCounter(counters map[string]int, key string) {
	if counters[key] <= 1 {
		delete(counters, key)
		return
	}
	counters[key]--
}

func addressHost(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err == nil {
		return strings.ToLower(host)
	}
	return strings.ToLower(addr.String())
}

func addressKey(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return strings.ToLower(addr.String())
}
