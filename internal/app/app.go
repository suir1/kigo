package app

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/spf13/cobra"
	"github.com/suir1/kigo/internal/advisor"
	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/discovery"
	"github.com/suir1/kigo/internal/netpolicy"
	"github.com/suir1/kigo/internal/netprobe"
	"github.com/suir1/kigo/internal/netproxy"
	"github.com/suir1/kigo/internal/netreuse"
	"github.com/suir1/kigo/internal/relay"
	"github.com/suir1/kigo/internal/service"
	"github.com/suir1/kigo/internal/transfer"
	"github.com/suir1/kigo/internal/transport"
	"github.com/suir1/kigo/internal/transport/webrtcx"
	"github.com/suir1/kigo/internal/version"
)

type globalOptions struct {
	Signal            string
	WebURL            string
	Transport         string
	SignalDirect      bool
	Relay             string
	RelayPass         string
	Proxy             string
	Interface         string
	AvoidVPN          bool
	NoAutoInterface   bool
	NoDirect          bool
	UDPProbe          bool
	NoTCPProbe        bool
	NoLAN             bool
	Local             bool
	DiscoveryAddr     string
	LANTimeout        time.Duration
	NoReconnect       bool
	NoNoteDrafts      bool
	ReconnectAttempts int
	ReconnectDelay    time.Duration
	PairTimeout       time.Duration
	DirectListen      string
	DirectAdvertise   string
	DirectTimeout     time.Duration
	Connections       int
	RouteHistory      string
	NoRouteHistory    bool
	Protocol          string
	TLSCA             string
	tlsConfig         *tls.Config
	networkPolicy     *netpolicy.Policy
	outboundSelection netpolicy.OutboundSelection
	outboundTarget    outboundProbeTarget
	allowLANUpgrade   bool
	taskOutput        *clientTaskOutput
}

type routeKind string

const (
	routeWebRTC              routeKind = "webrtc"
	routeSignalDirect        routeKind = "signal-direct"
	routeRelayOnly           routeKind = "relay"
	routeDirectRelayFallback routeKind = "direct-relay-fallback"
)

type routePlan struct {
	Kind        routeKind `json:"kind"`
	Primary     string    `json:"primary"`
	Fallback    string    `json:"fallback"`
	Description string    `json:"description"`
}

func NewRootCommand() *cobra.Command {
	var g globalOptions
	root := &cobra.Command{
		Use:           "kigo",
		Short:         "Native/web file and text transfer",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		clientCommand := clientRuntimeCommand(cmd)
		if clientCommand {
			if err := applyClientRuntimeConfig(cmd, &g); err != nil {
				return err
			}
		}
		if _, err := netproxy.Parse(g.Proxy); err != nil {
			return err
		}
		if err := configureClientTLS(&g); err != nil {
			return err
		}
		if err := validatePairTimeout(g.PairTimeout); err != nil {
			return err
		}
		if err := configureNetworkPolicy(cmd.Context(), &g, clientCommand); err != nil {
			return err
		}
		return validateDirectListenPolicy(&g)
	}
	root.PersistentFlags().StringVar(&g.Signal, "signal", defaultSignalURL, "signaling service URL")
	root.PersistentFlags().StringVar(&g.WebURL, "web-url", defaultWebURL, "public web URL shown in share links")
	root.PersistentFlags().StringVar(&g.Transport, "transport", transportModeAuto, "transport policy: auto, native, or webrtc")
	root.PersistentFlags().StringVar(&g.Relay, "relay", "", "native TCP relay candidate used by auto/native transport")
	root.PersistentFlags().StringVar(&g.RelayPass, "relay-pass", "", "password for native TCP relay")
	root.PersistentFlags().StringVar(&g.Proxy, "proxy", "", "proxy for external native relay connections (http://host:port or socks5://host:port)")
	root.PersistentFlags().StringVar(&g.TLSCA, "tls-ca", "", "PEM CA bundle additionally trusted for signaling HTTPS")
	root.PersistentFlags().StringVar(&g.Interface, "interface", "", "network interface used for client signaling and transfer traffic")
	root.PersistentFlags().BoolVar(&g.AvoidVPN, "avoid-vpn", false, "prefer a non-VPN physical interface for external client traffic")
	root.PersistentFlags().BoolVar(&g.NoAutoInterface, "no-auto-interface", false, "disable automatic VPN-aware outbound interface selection")
	root.PersistentFlags().BoolVar(&g.NoDirect, "no-direct", false, "disable native direct TCP attempt when using --relay")
	root.PersistentFlags().BoolVar(&g.UDPProbe, "udp-probe", false, "probe UDP NAT mapping and add authenticated UDP assistance to native direct")
	root.PersistentFlags().BoolVar(&g.NoTCPProbe, "no-tcp-probe", false, "disable relay-observed same-port TCP public candidate probing")
	root.PersistentFlags().BoolVar(&g.NoLAN, "no-lan", false, "disable embedded relay, LAN discovery, and relay-to-LAN upgrade")
	root.PersistentFlags().BoolVar(&g.Local, "local", false, "use only a sender-hosted or discovered LAN relay")
	root.PersistentFlags().StringVar(&g.DiscoveryAddr, "discovery-addr", discovery.DefaultAddr, "UDP address used for LAN relay discovery")
	root.PersistentFlags().DurationVar(&g.LANTimeout, "lan-discovery-timeout", 500*time.Millisecond, "time spent collecting LAN relay announcements")
	root.PersistentFlags().BoolVar(&g.NoReconnect, "no-reconnect", false, "disable automatic reconnect for native transfers and notepads")
	root.PersistentFlags().BoolVar(&g.NoNoteDrafts, "no-note-drafts", false, "disable encrypted local notepad draft persistence")
	root.PersistentFlags().IntVar(&g.ReconnectAttempts, "reconnect-attempts", 3, "total native transfer or notepad attempts including the first")
	root.PersistentFlags().DurationVar(&g.ReconnectDelay, "reconnect-delay", time.Second, "delay before retrying an interrupted native transfer or notepad")
	root.PersistentFlags().DurationVar(&g.PairTimeout, "pair-timeout", defaultPairTimeout, "maximum time to wait for the peer to enter the pairing code")
	root.PersistentFlags().StringVar(&g.DirectListen, "direct-listen", ":0", "native direct TCP listen address for senders")
	root.PersistentFlags().StringVar(&g.DirectAdvertise, "direct-advertise", "", "native direct TCP address or comma-separated candidates advertised through rendezvous")
	root.PersistentFlags().DurationVar(&g.DirectTimeout, "direct-timeout", 900*time.Millisecond, "native direct TCP attempt timeout before relay fallback")
	root.PersistentFlags().IntVar(&g.Connections, "connections", 4, "parallel native relay connections (1-8)")
	root.PersistentFlags().StringVar(&g.RouteHistory, "route-history", defaultRouteHistoryPath(), "local route history file")
	root.PersistentFlags().BoolVar(&g.NoRouteHistory, "no-route-history", false, "disable local route history recording and scoring")
	root.AddCommand(newServeCommand(), newLocalWebCommand(&g), newTUICommand(&g), newRelayCommand(&g), newSendCommand(&g), newRecvCommand(&g), newTextCommand(&g), newNoteCommand(&g), newDoctorCommand(&g), newRouteCommand(&g), newConfigCommand(), newVersionCommand())
	return root
}

func FormatError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errPairTimeout) {
		return err.Error() + "; start a new transfer and enter the fresh code promptly"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out; check that both sides are online and try again"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, transport.ErrClosed) || errors.Is(err, net.ErrClosed) {
		return "connection closed before the transfer completed; automatic same-code reconnect was exhausted"
	}
	message := err.Error()
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "relay password required"):
		return "relay password rejected; check --relay-pass or KIGO_RELAY_PASS"
	case strings.Contains(lower, "relay room expired") || strings.Contains(lower, "room expired"):
		return "pairing room expired; start a new transfer to get a fresh code"
	case strings.Contains(lower, "sender already waiting") || strings.Contains(lower, "receiver already waiting") || strings.Contains(lower, "room is full") || strings.Contains(lower, "room is locked"):
		return "pairing code is already in use; start a new transfer and use the new code"
	case strings.Contains(lower, "connection refused"):
		return "connection refused; check that the signaling service or relay is running and reachable"
	case strings.Contains(lower, "no such host"):
		return "host not found; check the signaling or relay address"
	case strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "timed out"):
		return "timed out; check the code, network, and that both sides are online"
	case strings.Contains(lower, "websocket") && (strings.Contains(lower, "bad handshake") || strings.Contains(lower, "unexpected response")):
		return "signaling handshake failed; check the --signal URL and reverse proxy WebSocket support"
	case strings.Contains(lower, "websocket") || strings.Contains(lower, "signaling"):
		return "signaling connection failed; check the --signal URL and try again"
	case strings.Contains(lower, "use of closed network connection") || strings.Contains(lower, "broken pipe") || strings.Contains(lower, "connection reset by peer"):
		return "peer disconnected before the transfer completed; automatic same-code reconnect was exhausted"
	case strings.Contains(lower, "expected resume") || strings.Contains(lower, "expected complete"):
		return "peer disconnected or spoke an incompatible transfer protocol"
	default:
		return message
	}
}

func newRelayCommand(g *globalOptions) *cobra.Command {
	var listen string
	var roomTTL time.Duration
	var pass, tokenSecret string
	var noLANAnnounce bool
	cmd := &cobra.Command{
		Use:   "relay",
		Short: "Run a native TCP relay",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			listen = flagEnvString(cmd, "listen", listen, "KIGO_RELAY_LISTEN")
			pass = flagEnvString(cmd, "pass", pass, "KIGO_RELAY_PASS")
			tokenSecret = flagEnvString(cmd, "token-secret", tokenSecret, "KIGO_RELAY_TOKEN_SECRET")
			discoveryAddr := flagEnvString(cmd, "discovery-addr", g.DiscoveryAddr, "KIGO_DISCOVERY_ADDR")
			var err error
			roomTTL, err = flagEnvDuration(cmd, "room-ttl", roomTTL, "KIGO_RELAY_ROOM_TTL")
			if err != nil {
				return err
			}
			return relay.RunWithOptions(ctx, relay.RunOptions{
				Listen:        listen,
				WaitTTL:       roomTTL,
				Pass:          pass,
				TokenSecret:   tokenSecret,
				LANAnnounce:   !noLANAnnounce,
				DiscoveryAddr: discoveryAddr,
				Interface:     g.Interface,
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":9000", "TCP relay listen address")
	cmd.Flags().DurationVar(&roomTTL, "room-ttl", 10*time.Minute, "maximum time one side may wait for a peer")
	cmd.Flags().StringVar(&pass, "pass", "", "optional password required from relay clients")
	cmd.Flags().StringVar(&tokenSecret, "token-secret", "", "shared secret for temporary room-bound relay credentials")
	cmd.Flags().BoolVar(&noLANAnnounce, "no-lan-announce", false, "disable UDP LAN relay announcements")
	return cmd
}

func newServeCommand() *cobra.Command {
	var listen, webDir, publicURL, nativeRelay, nativeRelaySecret, turnURL, turnListen, turnPublicIP, turnUser, turnPass, turnSecret, turnRealm, tlsCert, tlsKey, trustedProxies, noteStore string
	var nativeRelayCredentialTTL, turnCredentialTTL, turnEgressWindow, noteTTL time.Duration
	var signalRequestsPerMinute, noteUpdatesPerMinute, turnCredentialsPerMinute, turnMaxAllocations, turnMaxAllocationsPerUser, turnMaxAllocationsPerIP, turnMinPort, turnMaxPort int
	var turnMaxEgressMiB, turnMaxEgressMiBPerUser, turnMaxEgressMiBPerIP int64
	var checkConfig bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web app and WebRTC signaling service",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			listen = flagEnvString(cmd, "listen", listen, "KIGO_LISTEN")
			webDir = flagEnvString(cmd, "web-dir", webDir, "KIGO_WEB_DIR")
			publicURL = flagEnvString(cmd, "public-url", publicURL, "KIGO_PUBLIC_URL")
			nativeRelay = flagEnvString(cmd, "native-relay", nativeRelay, "KIGO_NATIVE_RELAY")
			nativeRelaySecret = flagEnvString(cmd, "native-relay-secret", nativeRelaySecret, "KIGO_NATIVE_RELAY_SECRET")
			turnURL = flagEnvString(cmd, "turn", turnURL, "KIGO_TURN")
			turnListen = flagEnvString(cmd, "turn-listen", turnListen, "KIGO_TURN_LISTEN")
			turnPublicIP = flagEnvString(cmd, "turn-public-ip", turnPublicIP, "KIGO_TURN_PUBLIC_IP")
			turnUser = flagEnvString(cmd, "turn-user", turnUser, "KIGO_TURN_USER")
			turnPass = flagEnvString(cmd, "turn-pass", turnPass, "KIGO_TURN_PASS")
			turnSecret = flagEnvString(cmd, "turn-secret", turnSecret, "KIGO_TURN_SECRET")
			turnRealm = flagEnvString(cmd, "turn-realm", turnRealm, "KIGO_TURN_REALM")
			tlsCert = flagEnvString(cmd, "tls-cert", tlsCert, "KIGO_TLS_CERT")
			tlsKey = flagEnvString(cmd, "tls-key", tlsKey, "KIGO_TLS_KEY")
			trustedProxies = flagEnvString(cmd, "trusted-proxies", trustedProxies, "KIGO_TRUSTED_PROXIES")
			noteStore = flagEnvString(cmd, "note-store", noteStore, "KIGO_NOTE_STORE")
			var err error
			noteTTL, err = flagEnvDuration(cmd, "note-ttl", noteTTL, "KIGO_NOTE_TTL")
			if err != nil {
				return err
			}
			noteUpdatesPerMinute, err = flagEnvInt(cmd, "note-updates-per-minute", noteUpdatesPerMinute, "KIGO_NOTE_UPDATES_PER_MINUTE")
			if err != nil {
				return err
			}
			nativeRelayCredentialTTL, err = flagEnvDuration(cmd, "native-relay-credential-ttl", nativeRelayCredentialTTL, "KIGO_NATIVE_RELAY_CREDENTIAL_TTL")
			if err != nil {
				return err
			}
			signalRequestsPerMinute, err = flagEnvInt(cmd, "signal-requests-per-minute", signalRequestsPerMinute, "KIGO_SIGNAL_REQUESTS_PER_MINUTE")
			if err != nil {
				return err
			}
			turnCredentialTTL, err = flagEnvDuration(cmd, "turn-credential-ttl", turnCredentialTTL, "KIGO_TURN_CREDENTIAL_TTL")
			if err != nil {
				return err
			}
			turnCredentialsPerMinute, err = flagEnvInt(cmd, "turn-credentials-per-minute", turnCredentialsPerMinute, "KIGO_TURN_CREDENTIALS_PER_MINUTE")
			if err != nil {
				return err
			}
			turnMaxAllocations, err = flagEnvInt(cmd, "turn-max-allocations", turnMaxAllocations, "KIGO_TURN_MAX_ALLOCATIONS")
			if err != nil {
				return err
			}
			turnMaxAllocationsPerUser, err = flagEnvInt(cmd, "turn-max-allocations-per-user", turnMaxAllocationsPerUser, "KIGO_TURN_MAX_ALLOCATIONS_PER_USER")
			if err != nil {
				return err
			}
			turnMaxAllocationsPerIP, err = flagEnvInt(cmd, "turn-max-allocations-per-ip", turnMaxAllocationsPerIP, "KIGO_TURN_MAX_ALLOCATIONS_PER_IP")
			if err != nil {
				return err
			}
			turnMinPort, err = flagEnvInt(cmd, "turn-min-port", turnMinPort, "KIGO_TURN_MIN_PORT")
			if err != nil {
				return err
			}
			turnMaxPort, err = flagEnvInt(cmd, "turn-max-port", turnMaxPort, "KIGO_TURN_MAX_PORT")
			if err != nil {
				return err
			}
			turnEgressWindow, err = flagEnvDuration(cmd, "turn-egress-window", turnEgressWindow, "KIGO_TURN_EGRESS_WINDOW")
			if err != nil {
				return err
			}
			turnMaxEgressMiB, err = flagEnvInt64(cmd, "turn-max-egress-mib", turnMaxEgressMiB, "KIGO_TURN_MAX_EGRESS_MIB")
			if err != nil {
				return err
			}
			turnMaxEgressMiBPerUser, err = flagEnvInt64(cmd, "turn-max-egress-mib-per-user", turnMaxEgressMiBPerUser, "KIGO_TURN_MAX_EGRESS_MIB_PER_USER")
			if err != nil {
				return err
			}
			turnMaxEgressMiBPerIP, err = flagEnvInt64(cmd, "turn-max-egress-mib-per-ip", turnMaxEgressMiBPerIP, "KIGO_TURN_MAX_EGRESS_MIB_PER_IP")
			if err != nil {
				return err
			}
			turnMaxEgressBytes, err := mebibyteLimit(turnMaxEgressMiB, "--turn-max-egress-mib")
			if err != nil {
				return err
			}
			turnMaxEgressBytesPerUser, err := mebibyteLimit(turnMaxEgressMiBPerUser, "--turn-max-egress-mib-per-user")
			if err != nil {
				return err
			}
			turnMaxEgressBytesPerIP, err := mebibyteLimit(turnMaxEgressMiBPerIP, "--turn-max-egress-mib-per-ip")
			if err != nil {
				return err
			}
			server := service.New(service.Config{
				Listen:                    listen,
				WebDir:                    webDir,
				PublicURL:                 publicURL,
				NativeRelay:               nativeRelay,
				NativeRelaySecret:         nativeRelaySecret,
				NativeRelayCredentialTTL:  nativeRelayCredentialTTL,
				TURN:                      turnURL,
				TURNListen:                turnListen,
				TURNPublicIP:              turnPublicIP,
				TURNMinPort:               turnMinPort,
				TURNMaxPort:               turnMaxPort,
				TURNUsername:              turnUser,
				TURNCredential:            turnPass,
				TURNSecret:                turnSecret,
				TURNRealm:                 turnRealm,
				TURNCredentialTTL:         turnCredentialTTL,
				TURNCredentialsPerMinute:  turnCredentialsPerMinute,
				TURNMaxAllocations:        turnMaxAllocations,
				TURNMaxAllocationsPerUser: turnMaxAllocationsPerUser,
				TURNMaxAllocationsPerIP:   turnMaxAllocationsPerIP,
				TURNEgressWindow:          turnEgressWindow,
				TURNMaxEgressBytes:        turnMaxEgressBytes,
				TURNMaxEgressBytesPerUser: turnMaxEgressBytesPerUser,
				TURNMaxEgressBytesPerIP:   turnMaxEgressBytesPerIP,
				TLSCert:                   tlsCert,
				TLSKey:                    tlsKey,
				NoteStore:                 noteStore,
				NoteTTL:                   noteTTL,
				NoteUpdatesPerMinute:      noteUpdatesPerMinute,
				SignalRequestsPerMinute:   signalRequestsPerMinute,
				TrustedProxies:            trustedProxies,
			})
			if checkConfig {
				if err := server.Validate(); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
				return nil
			}
			return server.Run(ctx)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":9100", "HTTP listen address")
	cmd.Flags().StringVar(&webDir, "web-dir", "", "directory containing the web app; defaults to embedded assets")
	cmd.Flags().StringVar(&publicURL, "public-url", "", "public URL for generated links")
	cmd.Flags().StringVar(&nativeRelay, "native-relay", "", "public native TCP relay advertised during transport negotiation")
	cmd.Flags().StringVar(&nativeRelaySecret, "native-relay-secret", "", "shared secret used to issue temporary native relay credentials")
	cmd.Flags().DurationVar(&nativeRelayCredentialTTL, "native-relay-credential-ttl", 2*time.Hour, "temporary native relay credential lifetime")
	cmd.Flags().IntVar(&signalRequestsPerMinute, "signal-requests-per-minute", 60, "signaling, negotiation, and rendezvous requests per source IP per minute; -1 disables")
	cmd.Flags().StringVar(&turnURL, "turn", "", "optional TURN URL returned by /api/ice")
	cmd.Flags().StringVar(&turnListen, "turn-listen", "", "optional built-in TURN UDP listen address")
	cmd.Flags().StringVar(&turnPublicIP, "turn-public-ip", "", "public IP advertised by the built-in TURN server")
	cmd.Flags().IntVar(&turnMinPort, "turn-min-port", 0, "minimum UDP relay port for built-in TURN; 0 uses dynamic ports")
	cmd.Flags().IntVar(&turnMaxPort, "turn-max-port", 0, "maximum UDP relay port for built-in TURN; 0 uses dynamic ports")
	cmd.Flags().StringVar(&turnUser, "turn-user", "kigo", "username for configured or built-in TURN")
	cmd.Flags().StringVar(&turnPass, "turn-pass", "kigo-turn", "credential for configured or built-in TURN")
	cmd.Flags().StringVar(&turnSecret, "turn-secret", "", "shared secret used to issue temporary TURN REST credentials")
	cmd.Flags().StringVar(&turnRealm, "turn-realm", "kigo", "realm for built-in TURN")
	cmd.Flags().DurationVar(&turnCredentialTTL, "turn-credential-ttl", 2*time.Hour, "temporary TURN credential lifetime")
	cmd.Flags().IntVar(&turnCredentialsPerMinute, "turn-credentials-per-minute", 1200, "temporary TURN credential responses per source IP per minute; -1 disables")
	cmd.Flags().IntVar(&turnMaxAllocations, "turn-max-allocations", 1024, "maximum active built-in TURN allocations; -1 disables")
	cmd.Flags().IntVar(&turnMaxAllocationsPerUser, "turn-max-allocations-per-user", 4, "maximum active built-in TURN allocations per credential; -1 disables")
	cmd.Flags().IntVar(&turnMaxAllocationsPerIP, "turn-max-allocations-per-ip", 32, "maximum active built-in TURN allocations per source IP; -1 disables")
	cmd.Flags().DurationVar(&turnEgressWindow, "turn-egress-window", time.Hour, "refill window for built-in TURN egress quotas")
	cmd.Flags().Int64Var(&turnMaxEgressMiB, "turn-max-egress-mib", -1, "global built-in TURN egress quota in MiB per window; -1 disables")
	cmd.Flags().Int64Var(&turnMaxEgressMiBPerUser, "turn-max-egress-mib-per-user", -1, "built-in TURN egress quota per credential in MiB per window; -1 disables")
	cmd.Flags().Int64Var(&turnMaxEgressMiBPerIP, "turn-max-egress-mib-per-ip", -1, "built-in TURN egress quota per source IP in MiB per window; -1 disables")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate path")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS private key path")
	cmd.Flags().StringVar(&trustedProxies, "trusted-proxies", "", "comma-separated proxy IPs or CIDRs trusted to set X-Forwarded-For")
	cmd.Flags().StringVar(&noteStore, "note-store", "", "directory for encrypted persistent notepad snapshots; empty keeps snapshots in memory")
	cmd.Flags().DurationVar(&noteTTL, "note-ttl", 30*24*time.Hour, "persistent notepad lifetime after its latest update")
	cmd.Flags().IntVar(&noteUpdatesPerMinute, "note-updates-per-minute", 240, "persistent notepad updates per source IP per minute; -1 disables")
	cmd.Flags().BoolVar(&checkConfig, "check-config", false, "validate configuration and exit without listening")
	return cmd
}

func newSendCommand(g *globalOptions) *cobra.Command {
	var symlinkValue, customCode string
	var noGitIgnore, noQRCode bool
	var remember bool
	cmd := &cobra.Command{
		Use:   "send <path>",
		Short: "Send a file or directory over WebRTC or a native TCP relay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClientTask(
				withInterrupt(cmd.Context()),
				g,
				sendTaskRequest{
					Path:        args[0],
					Code:        customCode,
					Symlinks:    symlinkValue,
					NoGitIgnore: noGitIgnore,
					NoQRCode:    noQRCode,
					Remember:    remember,
				},
				newClientTaskOutput(cmd.OutOrStdout(), cmd.ErrOrStderr(), nil),
			)
		},
	}
	cmd.Flags().StringVar(&symlinkValue, "symlinks", string(transfer.SymlinkFollow), "symbolic link policy: follow or preserve")
	cmd.Flags().StringVar(&customCode, "code", "", "custom pairing code; random six-character code when omitted")
	cmd.Flags().BoolVar(&noGitIgnore, "no-gitignore", false, "include paths matched by .gitignore files")
	cmd.Flags().BoolVar(&noQRCode, "no-qrcode", false, "do not print a terminal QR code")
	cmd.Flags().BoolVar(&remember, "remember", false, "remember this path and send options for the TUI")
	return cmd
}

func newRecvCommand(g *globalOptions) *cobra.Command {
	var out string
	var conflictValue string
	var remember bool
	cmd := &cobra.Command{
		Use:   "recv <code>",
		Short: "Receive a file or text message over WebRTC or a native TCP relay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClientTask(
				withInterrupt(cmd.Context()),
				g,
				receiveTaskRequest{
					Code:       args[0],
					OutputDir:  out,
					OnConflict: conflictValue,
					Remember:   remember,
				},
				newClientTaskOutput(cmd.OutOrStdout(), cmd.ErrOrStderr(), nil),
			)
		},
	}
	cmd.Flags().StringVar(&out, "out", ".", "output directory")
	cmd.Flags().StringVar(&conflictValue, "on-conflict", string(transfer.ConflictOverwrite), "existing file policy: overwrite, skip, or rename")
	cmd.Flags().BoolVar(&remember, "remember", false, "remember this output directory and conflict policy for the TUI")
	return cmd
}

func newTextCommand(g *globalOptions) *cobra.Command {
	var noQRCode bool
	var customCode string
	textCmd := &cobra.Command{Use: "text", Short: "Send or receive text"}
	send := &cobra.Command{
		Use:   "send [text]",
		Short: "Send text over WebRTC or a native TCP relay",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := ""
			if len(args) == 1 {
				text = args[0]
			} else {
				data, err := io.ReadAll(bufio.NewReader(os.Stdin))
				if err != nil {
					return err
				}
				text = string(data)
			}
			return runClientTask(
				withInterrupt(cmd.Context()),
				g,
				textSendTaskRequest{Text: text, Code: customCode, NoQRCode: noQRCode},
				newClientTaskOutput(cmd.OutOrStdout(), cmd.ErrOrStderr(), nil),
			)
		},
	}
	recv := &cobra.Command{
		Use:   "recv <code>",
		Short: "Receive text over WebRTC or a native TCP relay",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClientTask(
				withInterrupt(cmd.Context()),
				g,
				textReceiveTaskRequest{Code: args[0], OutputDir: filepath.Clean(".")},
				newClientTaskOutput(cmd.OutOrStdout(), cmd.ErrOrStderr(), nil),
			)
		},
	}
	send.Flags().BoolVar(&noQRCode, "no-qrcode", false, "do not print a terminal QR code")
	send.Flags().StringVar(&customCode, "code", "", "custom pairing code; random six-character code when omitted")
	textCmd.AddCommand(send, recv)
	return textCmd
}

func newDoctorCommand(g *globalOptions) *cobra.Command {
	var timeout, aiTimeout time.Duration
	var jsonOutput, aiExplain bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check local routing and service configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClientTask(
				cmd.Context(),
				g,
				doctorTaskRequest{
					Timeout:   timeout,
					JSON:      jsonOutput,
					AIExplain: aiExplain,
					AITimeout: aiTimeout,
				},
				newClientTaskOutput(cmd.OutOrStdout(), cmd.ErrOrStderr(), nil),
			)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "maximum time spent on network checks")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "write machine-readable diagnostics as JSON")
	cmd.Flags().BoolVar(&aiExplain, "ai-explain", false, "rewrite the diagnosis through a configured BYOK AI endpoint")
	cmd.Flags().DurationVar(&aiTimeout, "ai-timeout", 8*time.Second, "maximum time spent requesting an AI explanation")
	return cmd
}

func newRouteCommand(g *globalOptions) *cobra.Command {
	var timeout, aiTimeout time.Duration
	var jsonOutput, aiExplain bool
	var pair string
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Show scored transfer route candidates",
		RunE: func(cmd *cobra.Command, args []string) error {
			pair = strings.ToLower(strings.TrimSpace(pair))
			if !validRoutePair(pair) {
				return fmt.Errorf("invalid --pair %q; use all, native-native, native-web, or web-web", pair)
			}
			report := buildDoctorReportWithExplanation(
				cmd.Context(), g, pair, timeout, aiExplain, aiTimeout,
			)
			out := filterRouteReport(report, pair)
			out.Assessment = report.Assessment
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			outWriter := cmd.OutOrStdout()
			fmt.Fprintln(outWriter, "kigo route")
			if pair != "all" {
				fmt.Fprintf(outWriter, "- pair: %s\n", pair)
			}
			fmt.Fprintf(outWriter, "- native CLI primary=%s fallback=%s\n", out.Route.Primary, out.Route.Fallback)
			fmt.Fprintf(outWriter, "  - %s\n", out.Route.Description)
			printDoctorNetwork(outWriter, out.Network)
			printDoctorHistory(outWriter, out.History)
			printRouteCandidates(outWriter, out.Routes)
			printRouteDiagnostics(outWriter, out.Matrix)
			printDoctorAssessment(outWriter, out.Assessment)
			for _, errText := range out.Errors {
				fmt.Fprintf(outWriter, "- warning: %s\n", errText)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Second, "maximum time spent checking route dependencies")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "write machine-readable route candidates as JSON")
	cmd.Flags().StringVar(&pair, "pair", "all", "route pair to show: all, native-native, native-web, or web-web")
	cmd.Flags().BoolVar(&aiExplain, "ai-explain", false, "rewrite the selected pair diagnosis through a configured BYOK AI endpoint")
	cmd.Flags().DurationVar(&aiTimeout, "ai-timeout", 8*time.Second, "maximum time spent requesting an AI explanation")
	return cmd
}

func newVersionCommand() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version and build information",
		RunE: func(cmd *cobra.Command, args []string) error {
			info := version.Get()
			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			}
			fmt.Printf("kigo %s\n", info.Version)
			fmt.Printf("commit: %s\n", info.Commit)
			fmt.Printf("built: %s\n", info.Date)
			fmt.Printf("runtime: %s %s/%s\n", info.Go, info.OS, info.Arch)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "write machine-readable version information as JSON")
	return cmd
}

func printShareTarget(g *globalOptions, code string) {
	if link := transferPublicLink(g, code); link != "" {
		taskLink(g, link)
		if g.Relay != "" {
			taskLinef(g, "Native relay candidate: %s", g.Relay)
		}
		return
	}
	if g.Local {
		taskLine(g, "Relay: embedded LAN relay/discovery")
		return
	}
	taskLinef(g, "Relay: %s", g.Relay)
}

func planNativeRoute(g *globalOptions) routePlan {
	directDisabled := nativeDirectDisabled(g)
	if g.SignalDirect && !directDisabled && !g.Local {
		fallback := "WebRTC DataChannel"
		if g.Relay != "" {
			fallback = "native TCP relay"
		}
		return routePlan{
			Kind:        routeSignalDirect,
			Primary:     "direct TCP via signaling rendezvous",
			Fallback:    fallback,
			Description: "native peers exchange direct candidates through signaling without sending payload through the rendezvous service",
		}
	}
	if g.Relay == "" && !g.Local {
		return routePlan{
			Kind:        routeWebRTC,
			Primary:     "WebRTC DataChannel",
			Fallback:    "ICE may use STUN/TURN when configured by signaling service",
			Description: "native CLI pairs through signaling and transfers over WebRTC",
		}
	}
	if directDisabled {
		primary := "native TCP relay"
		description := "native CLI sends all frames through the configured relay"
		if g.Local {
			primary = "sender-hosted or discovered LAN TCP relay"
			description = "the sender hosts a temporary LAN relay and the receiver discovers it"
		}
		return routePlan{
			Kind:        routeRelayOnly,
			Primary:     primary,
			Fallback:    "none",
			Description: description,
		}
	}
	if g.Local {
		return routePlan{
			Kind:        routeDirectRelayFallback,
			Primary:     "direct TCP via LAN relay rendezvous",
			Fallback:    "sender-hosted or discovered LAN relay",
			Description: "the sender hosts a temporary LAN relay, exchanges direct candidates, and retains that relay as fallback",
		}
	}
	return routePlan{
		Kind:        routeDirectRelayFallback,
		Primary:     "direct TCP via relay rendezvous",
		Fallback:    "native TCP relay",
		Description: "native CLI advertises direct candidates, retries a LAN upgrade when needed, then falls back to relay",
	}
}

func webRTCReconnectAllowed(g *globalOptions, reconnect *webrtcx.ReconnectState) bool {
	return planNativeRoute(g).Kind != routeWebRTC || reconnect.Supported()
}

func dialTransport(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	reconnect *webrtcx.ReconnectState,
) (transport.Transport, error) {
	if role != "sender" && role != "receiver" {
		return nil, errors.New("invalid transport role")
	}
	switch planNativeRoute(g).Kind {
	case routeSignalDirect:
		return dialSignaledDirect(ctx, g, roomToken, role, reconnect)
	case routeRelayOnly, routeDirectRelayFallback:
		return dialRelayTransport(ctx, g, roomToken, role)
	default:
		return dialWebRTCTransport(ctx, g, roomToken, role, reconnect)
	}
}

func dialWebRTCTransport(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	reconnect *webrtcx.ReconnectState,
) (transport.Transport, error) {
	if role != "sender" && role != "receiver" {
		return nil, errors.New("invalid WebRTC role")
	}
	started := time.Now()
	iceServers, err := fetchICEServersWithOptions(ctx, g.Signal, g)
	if err != nil {
		recordRouteFailure(g, historyRouteWebRTC, time.Since(started))
		return nil, err
	}
	options := webrtcx.Options{
		SignalBase:               g.Signal,
		RoomToken:                roomToken,
		ICEServers:               iceServers,
		Reconnect:                reconnect,
		Protocol:                 g.Protocol,
		DialContext:              webRTCDialContext(g),
		TLSClientConfig:          clientTLSConfig(g),
		InterfaceFilter:          webRTCInterfaceFilter(g),
		IPFilter:                 webRTCIPFilter(g),
		IncludeLoopbackCandidate: webRTCIncludeLoopbackCandidate(g),
	}
	var t transport.Transport
	if role == "sender" {
		t, err = webrtcx.DialSender(ctx, options)
	} else {
		t, err = webrtcx.DialReceiver(ctx, options)
	}
	if err != nil {
		recordRouteFailure(g, historyRouteWebRTC, time.Since(started))
		return nil, err
	}
	return observeRoute(g, t, historyRouteWebRTC, 1), nil
}

func dialSignaledDirect(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	reconnect *webrtcx.ReconnectState,
) (transport.Transport, error) {
	if role != "sender" && role != "receiver" {
		return nil, errors.New("invalid direct role")
	}
	var listener net.Listener
	var candidates []string
	var candidateMetadata []directcandidate.Candidate
	if localDirectPreference(g) == relay.DirectPreferencePrefer {
		ln, err := listenDirect(ctx, directListenAddress(g))
		if err != nil {
			taskLinef(g, "Direct listen unavailable: %v", err)
		} else {
			listener = ln
			candidates, candidateMetadata = advertisedDirectCandidateSet(g, ln.Addr())
			if publicCandidate, ok := probeRelayObservedDirectCandidate(ctx, g, roomToken, role, ln); ok {
				candidates, candidateMetadata = appendDirectCandidate(
					candidates,
					candidateMetadata,
					publicCandidate,
				)
				taskLinef(g, "TCP public probe: %s", publicCandidate.Address)
			}
			taskLinef(g, "Direct: %s", strings.Join(candidates, ", "))
		}
	}
	bidirectional := listener != nil && netreuse.Supported && len(candidates) > 0
	natReport, udpPuncher := prepareDirectUDP(ctx, g)
	if !bidirectional && udpPuncher != nil {
		_ = udpPuncher.Close()
		udpPuncher = nil
	}
	defer func() {
		if udpPuncher != nil {
			_ = udpPuncher.Close()
		}
	}()
	peer, err := exchangeDirectCapability(
		ctx,
		g,
		roomToken,
		role,
		candidates,
		candidateMetadata,
		bidirectional,
		directNATClass(natReport),
		udpPunchCandidates(udpPuncher),
	)
	if err != nil {
		if listener != nil {
			_ = listener.Close()
		}
		taskLinef(g, "Direct rendezvous fallback: %v", err)
		return dialAfterSignaledDirect(ctx, g, roomToken, role, reconnect, g.Relay != "", err)
	}
	useRelay := g.Relay != "" && peer.PeerRelayFallback
	directAvailable := len(peer.PeerCandidates) > 0
	if role == "sender" {
		directAvailable = listener != nil && len(candidates) > 0
	}
	if !directAvailable ||
		localDirectPreference(g) == relay.DirectPreferenceRelay ||
		peer.PeerPreference == relay.DirectPreferenceRelay {
		if listener != nil {
			_ = listener.Close()
		}
		taskLine(g, "Direct skipped: peer route preference")
		return dialAfterSignaledDirect(ctx, g, roomToken, role, reconnect, useRelay, errors.New("direct route was skipped"))
	}
	directTimeout, timeoutReason := directTimeoutForPeer(g, natReport, peer)
	if timeoutReason != "" {
		taskLinef(g, "Direct timeout: %s (%s)", directTimeout, timeoutReason)
	}
	started := time.Now()
	var direct transport.Transport
	var count int
	if bidirectional && peer.PeerBidirectional {
		taskLine(g, "Direct mode: synchronized bidirectional TCP")
		if peer.PeerUDPPunch {
			startDirectUDPPunch(
				ctx,
				g,
				udpPuncher,
				peer.PeerUDPCandidates,
				roomToken,
				role,
				time.UnixMilli(peer.PunchAtMillis),
			)
		}
		direct, count, err = connectBidirectionalDirectBundle(
			ctx,
			listener,
			role,
			time.UnixMilli(peer.PunchAtMillis),
			directConnectOptions{
				candidates:  peer.PeerCandidates,
				metadata:    peer.PeerCandidateMetadata,
				timeout:     directTimeout,
				roomToken:   roomToken,
				connections: g.Connections,
				dialContext: outboundDirectDialer(g),
			},
		)
	} else if role == "sender" {
		direct, count, err = acceptDirectBundle(ctx, listener, directTimeout, roomToken, g.Connections)
	} else {
		_ = listener.Close()
		direct, count, err = connectDirectBundle(ctx, directConnectOptions{
			candidates:  peer.PeerCandidates,
			metadata:    peer.PeerCandidateMetadata,
			timeout:     directTimeout,
			roomToken:   roomToken,
			connections: g.Connections,
			dialContext: outboundDirectDialer(g),
		})
	}
	if err == nil {
		taskLinef(g, "Route: direct via signaling, connections: %d", count)
		return observeRoute(g, direct, historyRouteDirect, count), nil
	}
	recordRouteFailure(g, historyRouteDirect, time.Since(started))
	taskLinef(g, "Direct fallback: %v", err)
	return dialAfterSignaledDirect(ctx, g, roomToken, role, reconnect, useRelay, err)
}

func dialAfterSignaledDirect(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	reconnect *webrtcx.ReconnectState,
	useRelay bool,
	directErr error,
) (transport.Transport, error) {
	if useRelay {
		return dialRelayTransport(ctx, relayOnlyOptions(g), roomToken, role)
	}
	mode, _ := normalizeTransportMode(g.Transport)
	if mode == transportModeNative {
		return nil, directErr
	}
	taskLine(g, "Route fallback: WebRTC")
	return dialWebRTCTransport(ctx, webRTCOptions(g), roomToken, role, reconnect)
}

func relayOnlyOptions(g *globalOptions) *globalOptions {
	resolved := cloneGlobalOptions(g)
	resolved.SignalDirect = false
	resolved.allowLANUpgrade = !nativeDirectDisabled(g)
	resolved.NoDirect = true
	return resolved
}

func dialRelayTransport(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
) (transport.Transport, error) {
	if err := relay.ValidateConnectionCount(g.Connections); err != nil {
		return nil, err
	}
	var embedded *embeddedLANRelay
	if role == "sender" && !g.NoLAN {
		var err error
		embedded, err = startEmbeddedLANRelay(ctx, g)
		if err != nil {
			if g.Local {
				return nil, err
			}
			taskLinef(g, "Embedded LAN relay unavailable: %v", err)
		} else {
			taskLinef(g, "Embedded LAN relay: %s", embedded.Addr)
		}
	}
	defer func() {
		if embedded != nil {
			embedded.Stop()
		}
	}()

	var listener net.Listener
	var directCandidates []string
	var directCandidateMetadata []directcandidate.Candidate
	shouldListenDirect := !nativeDirectDisabled(g) &&
		(role == "sender" || localDirectPreference(g) == relay.DirectPreferencePrefer)
	if shouldListenDirect {
		ln, err := listenDirect(ctx, directListenAddress(g))
		if err == nil {
			listener = ln
			directCandidates, directCandidateMetadata = advertisedDirectCandidateSet(g, ln.Addr())
			taskLinef(g, "Direct: %s", strings.Join(directCandidates, ", "))
		} else if role == "sender" {
			taskLinef(g, "Direct disabled: %v", err)
		} else {
			taskLinef(g, "Direct listen unavailable: %v", err)
		}
	}
	bidirectional := listener != nil && netreuse.Supported && len(directCandidates) > 0
	var udpPuncher *netprobe.UDPPuncher
	if bidirectional && g.UDPProbe {
		_, udpPuncher = prepareDirectUDP(ctx, g)
	}
	defer func() {
		if udpPuncher != nil {
			_ = udpPuncher.Close()
		}
	}()
	capabilities := []string{relay.CapabilityRouteChoiceV1}
	if bidirectional {
		capabilities = append(capabilities, relay.CapabilityBidirectionalDirectV1)
	}
	if udpPuncher != nil {
		capabilities = append(capabilities, relay.CapabilityUDPPunchV1)
	}
	if lanUpgradeEnabled(g) {
		capabilities = append(capabilities, relay.CapabilityLANUpgradeV1)
	}
	join := relay.JoinOptions{
		RoomToken:               roomToken,
		Role:                    role,
		ConnectionCount:         g.Connections,
		Pass:                    relayPass(g),
		Direct:                  firstString(directCandidates),
		DirectCandidates:        directCandidates,
		DirectCandidateMetadata: directCandidateMetadata,
		UDPCandidates:           udpPunchCandidates(udpPuncher),
		Capabilities:            capabilities,
		DirectPreference:        localDirectPreference(g),
	}
	if listener != nil && !g.NoTCPProbe && g.DirectAdvertise == "" {
		join.DirectProbeLocalPort = directListenerPort(listener)
	}
	relayStarted := time.Now()
	var embeddedCandidates []relay.Candidate
	if embedded != nil {
		embeddedCandidates = append(embeddedCandidates, relay.Candidate{
			Addr: embedded.Addr, Kind: "embedded", Priority: 0,
		})
	}
	result, err := raceRelayJoin(ctx, g, join, embeddedCandidates...)
	if err != nil {
		recordRouteFailure(g, historyRouteRelay, time.Since(relayStarted))
		if listener != nil {
			_ = listener.Close()
		}
		return nil, err
	}
	finishRelay := func() (transport.Transport, error) {
		t, count, historyKind, label, keepEmbedded := finalizeRelayRoute(
			ctx, g, roomToken, role, result, join, embedded,
		)
		if keepEmbedded {
			embedded = nil
		}
		taskLinef(g, "Route: %s, connections: %d", label, count)
		return observeRoute(g, t, historyKind, count), nil
	}
	if role == "sender" && listener == nil {
		return finishRelay()
	}
	if role == "receiver" &&
		(nativeDirectDisabled(g) || len(result.PeerDirectCandidates) == 0) {
		if listener != nil {
			_ = listener.Close()
		}
		return finishRelay()
	}
	if !peersShouldAttemptDirect(join, result.JoinResult) {
		if listener != nil {
			_ = listener.Close()
		}
		taskLinef(g, "Route preference: relay (%s)", relayPreferenceReason(join, result.JoinResult))
		return finishRelay()
	}

	directStarted := time.Now()
	var direct transport.Transport
	var count int
	if bidirectional &&
		hasCapability(result.PeerCapabilities, relay.CapabilityBidirectionalDirectV1) &&
		result.PunchAtMillis > 0 {
		taskLine(g, "Direct mode: synchronized bidirectional TCP")
		if udpPuncher != nil &&
			hasCapability(result.PeerCapabilities, relay.CapabilityUDPPunchV1) {
			startDirectUDPPunch(
				ctx,
				g,
				udpPuncher,
				result.PeerUDPCandidates,
				roomToken,
				role,
				time.UnixMilli(result.PunchAtMillis),
			)
		}
		direct, count, err = connectBidirectionalDirectBundle(
			ctx,
			listener,
			role,
			time.UnixMilli(result.PunchAtMillis),
			directConnectOptions{
				candidates:  result.PeerDirectCandidates,
				metadata:    result.PeerDirectCandidateMetadata,
				timeout:     g.DirectTimeout,
				roomToken:   roomToken,
				connections: g.Connections,
				dialContext: outboundDirectDialer(g),
			},
		)
	} else if role == "sender" {
		direct, count, err = acceptDirectBundle(ctx, listener, g.DirectTimeout, roomToken, g.Connections)
	} else {
		if listener != nil {
			_ = listener.Close()
		}
		direct, count, err = connectDirectBundle(ctx, directConnectOptions{
			candidates:  result.PeerDirectCandidates,
			metadata:    result.PeerDirectCandidateMetadata,
			timeout:     g.DirectTimeout,
			roomToken:   roomToken,
			connections: g.Connections,
			dialContext: outboundDirectDialer(g),
		})
	}
	if err != nil {
		recordRouteFailure(g, historyRouteDirect, time.Since(directStarted))
		taskLinef(g, "Direct fallback: %v", err)
		return finishRelay()
	}
	_ = result.Transport.Close()
	taskLinef(g, "Route: direct, connections: %d", count)
	return observeRoute(g, direct, historyRouteDirect, count), nil
}

func observeRoute(g *globalOptions, t transport.Transport, kind string, connections int) transport.Transport {
	weights := routePathWeights(g, kind, connections)
	if formatted := formatRoutePathWeights(weights); formatted != "" {
		taskLinef(g, "Historical path weights: %s", formatted)
	}
	return transport.Observe(t, transport.RouteInfo{
		Kind:        kind,
		Connections: connections,
		PathWeights: weights,
	})
}

func formatRoutePathWeights(weights []float64) string {
	var formatted []string
	nonDefault := false
	for connection := 1; connection < len(weights); connection++ {
		weight := clampRoutePathWeight(weights[connection])
		if weight != 1 {
			nonDefault = true
		}
		formatted = append(formatted, fmt.Sprintf("p%d=%.2f", connection, weight))
	}
	if !nonDefault {
		return ""
	}
	return strings.Join(formatted, " ")
}

func localDirectPreference(g *globalOptions) string {
	if nativeDirectDisabled(g) || shouldDeferDirectFromHistory(inspectRouteHistory(g), time.Now()) {
		return relay.DirectPreferenceRelay
	}
	return relay.DirectPreferencePrefer
}

func peersShouldAttemptDirect(local relay.JoinOptions, peer relay.JoinResult) bool {
	if !hasCapability(local.Capabilities, relay.CapabilityRouteChoiceV1) ||
		!hasCapability(peer.PeerCapabilities, relay.CapabilityRouteChoiceV1) {
		return true
	}
	return local.DirectPreference != relay.DirectPreferenceRelay &&
		peer.PeerDirectPreference != relay.DirectPreferenceRelay
}

func relayPreferenceReason(local relay.JoinOptions, peer relay.JoinResult) string {
	switch {
	case local.DirectPreference == relay.DirectPreferenceRelay &&
		peer.PeerDirectPreference == relay.DirectPreferenceRelay:
		return "both peers deferred direct"
	case local.DirectPreference == relay.DirectPreferenceRelay:
		return "local direct history"
	default:
		return "peer direct history"
	}
}

func hasCapability(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func raceRelayJoin(
	ctx context.Context,
	g *globalOptions,
	join relay.JoinOptions,
	extra ...relay.Candidate,
) (relay.RaceResult, error) {
	proxyConfig, err := outboundProxyConfig(g)
	if err != nil {
		return relay.RaceResult{}, err
	}
	if proxyConfig != nil {
		join.DialContext = proxyConfig.DialContext
	} else if selectedNetworkPolicy(g) != nil {
		join.DialContext = outboundDialContext(g)
	}
	candidates, err := relayCandidates(ctx, g, extra...)
	if err != nil {
		return relay.RaceResult{}, err
	}
	result, err := relay.RaceJoin(ctx, relay.RaceOptions{
		Candidates: candidates,
		Join:       join,
	})
	if err != nil {
		return relay.RaceResult{}, err
	}
	taskLinef(g, "Relay route: %s (%s)", result.Candidate.Kind, result.Candidate.Addr)
	return result, nil
}

func relayCandidates(ctx context.Context, g *globalOptions, extra ...relay.Candidate) ([]relay.Candidate, error) {
	if g.Local && g.NoLAN {
		return nil, errors.New("--local cannot be used with --no-lan")
	}
	candidates := append([]relay.Candidate(nil), extra...)
	hasEmbedded := false
	for _, candidate := range extra {
		if candidate.Kind == "embedded" {
			hasEmbedded = true
			break
		}
	}
	if !g.NoLAN && !hasEmbedded {
		timeout := g.LANTimeout
		if timeout <= 0 {
			return nil, errors.New("LAN discovery timeout must be positive")
		}
		addresses, err := discovery.DiscoverOnInterface(ctx, g.DiscoveryAddr, timeout, g.Interface)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			if g.Local {
				return nil, fmt.Errorf("LAN relay discovery failed: %w", err)
			}
			taskLinef(g, "LAN discovery unavailable: %v", err)
		}
		for _, addr := range addresses {
			candidates = append(candidates, relay.Candidate{
				Addr:     addr,
				Kind:     "lan",
				Priority: 0,
				UseProxy: false,
			})
		}
	}
	if !g.Local && g.Relay != "" {
		delay := time.Duration(0)
		if len(candidates) > 0 {
			delay = 180 * time.Millisecond
		}
		candidates = append(candidates, relay.Candidate{
			Addr:       g.Relay,
			Kind:       "external",
			Priority:   1,
			StartDelay: delay,
			UseProxy:   strings.TrimSpace(g.Proxy) != "",
		})
	}
	if len(candidates) == 0 {
		if g.Local {
			return nil, errors.New("no LAN relay discovered")
		}
		return nil, errors.New("no native relay candidates")
	}
	return candidates, nil
}

func advertisedDirectAddr(g *globalOptions, addr net.Addr) string {
	return firstString(advertisedDirectCandidates(g, addr))
}

func advertisedDirectCandidates(g *globalOptions, addr net.Addr) []string {
	addresses, _ := advertisedDirectCandidateSet(g, addr)
	return addresses
}

func advertisedDirectCandidateSet(g *globalOptions, addr net.Addr) ([]string, []directcandidate.Candidate) {
	var addresses []string
	manual := false
	if g.DirectAdvertise != "" {
		addresses = uniqueCandidates(strings.Split(g.DirectAdvertise, ","))
		manual = true
	} else {
		host, port, err := net.SplitHostPort(addr.String())
		switch {
		case err != nil:
			addresses = []string{addr.String()}
		case host != "" && host != "::" && host != "0.0.0.0":
			addresses = []string{net.JoinHostPort(host, port)}
		default:
			hosts := policyAdvertiseHosts(g)
			addresses = make([]string, 0, len(hosts))
			for _, candidateHost := range hosts {
				addresses = append(addresses, net.JoinHostPort(candidateHost, port))
			}
		}
	}
	metadata := make([]directcandidate.Candidate, 0, len(addresses))
	for _, address := range addresses {
		candidate, err := directcandidate.FromAddress(address, manual)
		if err == nil {
			metadata = append(metadata, candidate)
		}
	}
	metadata = directcandidate.Merge(addresses, metadata)
	return directcandidate.Addresses(metadata), metadata
}

func chooseAdvertiseHost() string {
	return firstString(advertiseHosts())
}

func advertiseHosts() []string {
	var ipv4, ipv6 []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return []string{"127.0.0.1"}
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := ipFromAddr(addr)
			if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				ipv4 = append(ipv4, ip4.String())
				continue
			}
			ipv6 = append(ipv6, ip.String())
		}
	}
	hosts := append(ipv4, ipv6...)
	if len(hosts) == 0 {
		return []string{"127.0.0.1"}
	}
	return uniqueStrings(hosts)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func ipFromAddr(addr net.Addr) net.IP {
	switch v := addr.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err == nil {
			return net.ParseIP(host)
		}
		return net.ParseIP(addr.String())
	}
}

func relayPass(g *globalOptions) string {
	if g.RelayPass != "" {
		return g.RelayPass
	}
	return os.Getenv("KIGO_RELAY_PASS")
}

func flagEnvString(cmd *cobra.Command, flagName, value, envName string) string {
	if cmd.Flags().Changed(flagName) {
		return value
	}
	if envValue := os.Getenv(envName); envValue != "" {
		return envValue
	}
	return value
}

func flagEnvParsed[T any](
	cmd *cobra.Command,
	flagName string,
	value T,
	envName string,
	parse func(string) (T, error),
) (T, error) {
	if cmd.Flags().Changed(flagName) {
		return value, nil
	}
	envValue := os.Getenv(envName)
	if envValue == "" {
		return value, nil
	}
	parsed, err := parse(envValue)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%s: %w", envName, err)
	}
	return parsed, nil
}

func flagEnvDuration(cmd *cobra.Command, flagName string, value time.Duration, envName string) (time.Duration, error) {
	return flagEnvParsed(cmd, flagName, value, envName, time.ParseDuration)
}

func flagEnvInt(cmd *cobra.Command, flagName string, value int, envName string) (int, error) {
	return flagEnvParsed(cmd, flagName, value, envName, strconv.Atoi)
}

func flagEnvInt64(cmd *cobra.Command, flagName string, value int64, envName string) (int64, error) {
	return flagEnvParsed(cmd, flagName, value, envName, func(raw string) (int64, error) {
		return strconv.ParseInt(raw, 10, 64)
	})
}

func mebibyteLimit(value int64, name string) (int64, error) {
	const mebibyte = int64(1 << 20)
	const maxInt64 = int64(^uint64(0) >> 1)
	if value == -1 {
		return -1, nil
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be -1 or positive", name)
	}
	if value > maxInt64/mebibyte {
		return 0, fmt.Errorf("%s is too large", name)
	}
	return value * mebibyte, nil
}

type iceConfig struct {
	ICEServers []struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	} `json:"iceServers"`
}

type doctorReport struct {
	OK         bool                `json:"ok"`
	Version    version.Info        `json:"version"`
	Network    doctorNetworkReport `json:"network"`
	Signal     doctorSignalReport  `json:"signal"`
	STUN       netprobe.STUNReport `json:"stun_probe"`
	LAN        doctorLANReport     `json:"lan"`
	Relay      doctorRelayReport   `json:"relay"`
	Direct     doctorDirectReport  `json:"direct"`
	History    doctorHistoryReport `json:"history"`
	Route      routePlan           `json:"route"`
	Routes     []routeCandidate    `json:"routes"`
	Matrix     []doctorMatrixEntry `json:"matrix"`
	Assessment doctorAssessment    `json:"assessment"`
	Errors     []string            `json:"errors,omitempty"`
}

type routeReport struct {
	OK         bool                `json:"ok"`
	Version    version.Info        `json:"version"`
	Network    doctorNetworkReport `json:"network"`
	Route      routePlan           `json:"route"`
	STUN       netprobe.STUNReport `json:"stun_probe"`
	History    doctorHistoryReport `json:"history"`
	Routes     []routeCandidate    `json:"routes"`
	Matrix     []doctorMatrixEntry `json:"matrix"`
	Assessment doctorAssessment    `json:"assessment"`
	Errors     []string            `json:"errors,omitempty"`
}

type doctorNetworkReport struct {
	Policy            string            `json:"policy"`
	Path              string            `json:"path"`
	Reason            string            `json:"reason"`
	Interface         string            `json:"interface,omitempty"`
	Addresses         []string          `json:"addresses,omitempty"`
	VPNDetected       bool              `json:"vpn_detected"`
	PreferredPhysical string            `json:"preferred_physical,omitempty"`
	ProbeTargetKind   string            `json:"probe_target_kind,omitempty"`
	ProbeTarget       string            `json:"probe_target,omitempty"`
	Probes            []netpolicy.Probe `json:"probes,omitempty"`
}

type doctorSignalReport struct {
	OK            bool              `json:"ok"`
	BaseURL       string            `json:"base_url"`
	APIURL        string            `json:"api_url,omitempty"`
	LatencyMillis int64             `json:"latency_ms,omitempty"`
	ICE           iceSummary        `json:"ice"`
	Servers       []doctorICEServer `json:"servers,omitempty"`
	Capabilities  []string          `json:"capabilities,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	Error         string            `json:"error,omitempty"`
}

type doctorICEServer struct {
	URLs          []string `json:"urls"`
	AuthProvided  bool     `json:"auth_provided"`
	Username      string   `json:"username,omitempty"`
	CredentialSet bool     `json:"credential_set,omitempty"`
}

type doctorRelayReport struct {
	Configured    bool   `json:"configured"`
	OK            bool   `json:"ok"`
	Addr          string `json:"addr,omitempty"`
	ViaProxy      bool   `json:"via_proxy,omitempty"`
	LatencyMillis int64  `json:"latency_ms,omitempty"`
	Error         string `json:"error,omitempty"`
}

type doctorLANReport struct {
	Enabled bool     `json:"enabled"`
	OK      bool     `json:"ok"`
	Address string   `json:"address,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
	Relays  []string `json:"relays,omitempty"`
	Error   string   `json:"error,omitempty"`
}

type doctorDirectReport struct {
	Enabled           bool   `json:"enabled"`
	OK                bool   `json:"ok"`
	DisabledReason    string `json:"disabled_reason,omitempty"`
	Listen            string `json:"listen,omitempty"`
	Advertise         string `json:"advertise,omitempty"`
	Timeout           string `json:"timeout,omitempty"`
	SamePortSupported bool   `json:"same_port_supported"`
	TCPProbeEnabled   bool   `json:"tcp_probe_enabled,omitempty"`
	PublicAddress     string `json:"public_address,omitempty"`
	PublicProbeError  string `json:"public_probe_error,omitempty"`
	Error             string `json:"error,omitempty"`
}

type doctorMatrixEntry struct {
	Pair     string `json:"pair"`
	Primary  string `json:"primary"`
	Fallback string `json:"fallback,omitempty"`
}

type doctorAssessment struct {
	Diagnosis          string                `json:"diagnosis"`
	ExplanationSource  string                `json:"explanation_source"`
	ExplanationWarning string                `json:"explanation_warning,omitempty"`
	Recommendation     string                `json:"recommendation"`
	Actions            []string              `json:"actions,omitempty"`
	RouteResult        doctorRouteResultHint `json:"route_result_hint"`
}

type doctorRouteResultHint struct {
	Path                    string `json:"path"`
	Reason                  string `json:"reason"`
	DirectAttempted         bool   `json:"direct_attempted"`
	DataRelayRequired       bool   `json:"data_relay_required"`
	RendezvousRelayRequired bool   `json:"rendezvous_relay_required"`
}

type routeCandidate struct {
	Pair      string               `json:"pair"`
	Kind      routeKind            `json:"kind"`
	Name      string               `json:"name"`
	Score     int                  `json:"score"`
	Available bool                 `json:"available"`
	Primary   bool                 `json:"primary,omitempty"`
	Fallback  string               `json:"fallback,omitempty"`
	Requires  []string             `json:"requires,omitempty"`
	Reasons   []string             `json:"reasons,omitempty"`
	Warnings  []string             `json:"warnings,omitempty"`
	History   *routeHistorySummary `json:"history,omitempty"`
	Probe     *routeProbeReport    `json:"probe,omitempty"`
}

type routeProbeReport struct {
	Kind          string `json:"kind"`
	OK            bool   `json:"ok"`
	LatencyMillis int64  `json:"latency_ms,omitempty"`
}

func fetchSignalJSON(ctx context.Context, signalBase, path string, g *globalOptions, out any) (string, int64, error) {
	endpoint, err := apiURL(signalBase, path)
	if err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return endpoint, 0, err
	}
	started := time.Now()
	res, err := outboundHTTPClient(g).Do(req)
	latencyMillis := elapsedMillis(started)
	if err != nil {
		return endpoint, latencyMillis, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return endpoint, latencyMillis, fmt.Errorf("%s returned %s", endpoint, res.Status)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out); err != nil {
		return endpoint, latencyMillis, err
	}
	return endpoint, latencyMillis, nil
}

func fetchICEServersWithOptions(ctx context.Context, signalBase string, g *globalOptions) ([]webrtc.ICEServer, error) {
	var cfg iceConfig
	if _, _, err := fetchSignalJSON(ctx, signalBase, "/api/ice", g, &cfg); err != nil {
		return nil, err
	}
	servers := make([]webrtc.ICEServer, 0, len(cfg.ICEServers))
	for _, server := range cfg.ICEServers {
		if len(server.URLs) == 0 {
			continue
		}
		servers = append(servers, webrtc.ICEServer{
			URLs:       server.URLs,
			Username:   server.Username,
			Credential: server.Credential,
		})
	}
	if len(servers) == 0 {
		return webrtcx.DefaultICEServers(), nil
	}
	return servers, nil
}

func printDoctorReport(out io.Writer, report doctorReport) error {
	fmt.Fprintln(out, "kigo doctor")
	printDoctorNetwork(out, report.Network)
	printDoctorSignal(out, report.Signal)
	printDoctorSTUN(out, report.STUN)
	printDoctorLAN(out, report.LAN)
	printDoctorRelay(out, report.Relay)
	printDoctorDirect(out, report.Direct)
	printDoctorHistory(out, report.History)
	fmt.Fprintf(out, "- route plan: native CLI primary=%s fallback=%s\n", report.Route.Primary, report.Route.Fallback)
	fmt.Fprintf(out, "  - %s\n", report.Route.Description)
	printRouteCandidates(out, report.Routes)
	printRouteDiagnostics(out, report.Matrix)
	printDoctorAssessment(out, report.Assessment)
	if len(report.Errors) > 0 {
		return errors.New(strings.Join(report.Errors, "; "))
	}
	return nil
}

func writeDoctorJSON(out io.Writer, report doctorReport) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return err
	}
	if len(report.Errors) > 0 {
		return errors.New(strings.Join(report.Errors, "; "))
	}
	return nil
}

func buildDoctorReport(ctx context.Context, g *globalOptions) doctorReport {
	plan := planNativeRoute(g)
	signal := inspectSignalWithOptions(ctx, g.Signal, g)
	stunReport := inspectSTUNWithOptions(ctx, signal, g)
	lan := inspectLAN(ctx, g)
	relay := inspectRelayWithOptions(ctx, g.Relay, g.Proxy, g)
	direct := inspectDirect(ctx, g)
	history := inspectRouteHistory(g)
	routeRelay := relay
	if lan.OK {
		routeRelay.Configured = true
		routeRelay.OK = true
		routeRelay.Addr = lan.Relays[0]
		routeRelay.Error = ""
	}
	routes := planRouteCandidates(signal, routeRelay, direct, stunReport, g)
	applyRouteHistory(routes, history)
	report := doctorReport{
		Version: version.Get(),
		Network: inspectNetworkPolicy(g),
		Signal:  signal,
		STUN:    stunReport,
		LAN:     lan,
		Relay:   relay,
		Direct:  direct,
		History: history,
		Route:   plan,
		Routes:  routes,
		Matrix:  doctorMatrix(routes),
	}
	if !signal.OK && signal.Error != "" {
		report.Errors = append(report.Errors, "signaling: "+signal.Error)
	}
	if relay.Configured && !relay.OK && relay.Error != "" {
		report.Errors = append(report.Errors, "relay: "+relay.Error)
	}
	if g.Local && !lan.OK {
		message := "no LAN relay discovered"
		if lan.Error != "" {
			message = lan.Error
		}
		report.Errors = append(report.Errors, "LAN: "+message)
	}
	if direct.Enabled && !direct.OK && direct.Error != "" {
		report.Errors = append(report.Errors, "direct: "+direct.Error)
	}
	report.OK = len(report.Errors) == 0
	report.Assessment = assessDoctorPair(report, "native-native")
	return report
}

func buildDoctorReportWithExplanation(
	ctx context.Context,
	g *globalOptions,
	pair string,
	timeout time.Duration,
	explain bool,
	explainTimeout time.Duration,
) doctorReport {
	doctorCtx, cancel := context.WithTimeout(ctx, timeout)
	report := buildDoctorReport(doctorCtx, g)
	cancel()
	report.Assessment = assessDoctorPair(report, pair)
	if !explain {
		return report
	}
	aiCtx, aiCancel := context.WithTimeout(ctx, explainTimeout)
	applyAIExplanation(aiCtx, &report, pair, advisorConfigFromEnv())
	aiCancel()
	return report
}

func filterRouteReport(report doctorReport, pair string) routeReport {
	out := routeReport{
		OK:         report.OK,
		Version:    report.Version,
		Network:    report.Network,
		Route:      report.Route,
		STUN:       report.STUN,
		History:    report.History,
		Routes:     report.Routes,
		Matrix:     report.Matrix,
		Assessment: report.Assessment,
		Errors:     report.Errors,
	}
	if pair == "all" {
		return out
	}
	out.Routes = filterRouteCandidates(report.Routes, pair)
	out.Matrix = filterRouteMatrix(report.Matrix, pair)
	out.Assessment = assessDoctorPair(report, pair)
	return out
}

func filterRouteCandidates(routes []routeCandidate, pair string) []routeCandidate {
	filtered := make([]routeCandidate, 0, len(routes))
	for _, route := range routes {
		if route.Pair == pair {
			filtered = append(filtered, route)
		}
	}
	return filtered
}

func filterRouteMatrix(matrix []doctorMatrixEntry, pair string) []doctorMatrixEntry {
	filtered := make([]doctorMatrixEntry, 0, len(matrix))
	for _, entry := range matrix {
		if entry.Pair == pair {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func validRoutePair(pair string) bool {
	switch pair {
	case "all", "native-native", "native-web", "web-web":
		return true
	default:
		return false
	}
}

type iceSummary struct {
	Total           int `json:"total"`
	STUN            int `json:"stun"`
	TURN            int `json:"turn"`
	TURNWithAuth    int `json:"turn_with_auth"`
	TURNWithoutAuth int `json:"turn_without_auth"`
}

func inspectSignalWithOptions(ctx context.Context, signalBase string, g *globalOptions) doctorSignalReport {
	report := doctorSignalReport{BaseURL: signalBase}
	var cfg iceConfig
	var err error
	report.APIURL, report.LatencyMillis, err = fetchSignalJSON(ctx, signalBase, "/api/ice", g, &cfg)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.OK = true
	report.ICE = summarizeICE(cfg)
	report.Capabilities = inspectSignalCapabilitiesWithOptions(ctx, signalBase, g)
	for _, server := range cfg.ICEServers {
		if len(server.URLs) > 0 {
			report.Servers = append(report.Servers, doctorICEServer{
				URLs:          server.URLs,
				AuthProvided:  server.Username != "" || server.Credential != "",
				Username:      server.Username,
				CredentialSet: server.Credential != "",
			})
		}
	}
	if report.ICE.TURN == 0 {
		report.Warnings = append(report.Warnings, "no TURN server advertised; browser-browser may fail on restrictive networks")
	}
	if report.ICE.TURNWithoutAuth > 0 {
		report.Warnings = append(report.Warnings, "TURN server advertised without username/credential")
	}
	return report
}

func inspectSignalCapabilitiesWithOptions(ctx context.Context, signalBase string, g *globalOptions) []string {
	var body struct {
		Capabilities []string `json:"capabilities"`
	}
	if _, _, err := fetchSignalJSON(ctx, signalBase, "/api/health", g, &body); err != nil {
		return nil
	}
	return uniqueStrings(body.Capabilities)
}

func printDoctorSignal(out io.Writer, report doctorSignalReport) {
	if !report.OK {
		fmt.Fprintf(out, "- signaling: failed (%s)\n", report.Error)
		return
	}
	fmt.Fprintf(out, "- signaling: ok %s latency=%dms (%d ICE server%s, STUN=%d TURN=%d)\n", report.APIURL, report.LatencyMillis, report.ICE.Total, plural(report.ICE.Total), report.ICE.STUN, report.ICE.TURN)
	for _, server := range report.Servers {
		auth := ""
		if server.AuthProvided {
			auth = " auth=configured"
		}
		fmt.Fprintf(out, "  - ice: %s%s\n", strings.Join(server.URLs, ", "), auth)
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(out, "  - warning: %s\n", warning)
	}
}

func summarizeICE(cfg iceConfig) iceSummary {
	var summary iceSummary
	for _, server := range cfg.ICEServers {
		for _, raw := range server.URLs {
			u := strings.ToLower(raw)
			switch {
			case strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:"):
				summary.STUN++
			case strings.HasPrefix(u, "turn:") || strings.HasPrefix(u, "turns:"):
				summary.TURN++
				if server.Username != "" && server.Credential != "" {
					summary.TURNWithAuth++
				} else {
					summary.TURNWithoutAuth++
				}
			}
			summary.Total++
		}
	}
	return summary
}

func inspectSTUNWithOptions(ctx context.Context, signal doctorSignalReport, g *globalOptions) netprobe.STUNReport {
	if !signal.OK {
		return netprobe.STUNReport{
			Class: netprobe.NATUnknown,
			Error: "signaling ICE configuration unavailable",
		}
	}
	var urls []string
	for _, server := range signal.Servers {
		urls = append(urls, server.URLs...)
	}
	return netprobe.ProbeSTUNWithOptions(ctx, urls, 800*time.Millisecond, outboundSTUNOptions(g))
}

func printDoctorSTUN(out io.Writer, report netprobe.STUNReport) {
	if !report.OK {
		fmt.Fprintf(out, "- STUN NAT probe: unavailable (%s)\n", report.Error)
		return
	}
	fmt.Fprintf(out, "- STUN NAT probe: %s local=%s\n", report.Class, report.Local)
	for _, observation := range report.Observations {
		if observation.Error != "" {
			fmt.Fprintf(out, "  - %s failed: %s\n", observation.Server, observation.Error)
			continue
		}
		fmt.Fprintf(
			out,
			"  - %s mapped=%s latency=%dms\n",
			observation.Server,
			observation.Mapped,
			observation.LatencyMillis,
		)
	}
	if report.Class == netprobe.NATUnknown && len(report.Observations) == 1 {
		fmt.Fprintln(out, "  - warning: a second STUN destination is needed to classify mapping behavior")
	}
}

func planRouteCandidates(
	signal doctorSignalReport,
	relay doctorRelayReport,
	direct doctorDirectReport,
	stunReport netprobe.STUNReport,
	g *globalOptions,
) []routeCandidate {
	var routes []routeCandidate
	directDisabled := nativeDirectDisabled(g)
	if !g.Local && !directDisabled {
		routes = append(routes, signalingDirectCandidate(signal, relay, direct, stunReport))
	}
	if g.Relay == "" && !g.Local {
		routes = append(routes, webrtcCandidate("native-native", signal, stunReport))
	} else {
		if !directDisabled {
			routes = append(routes, directRelayFallbackCandidate(relay, direct, stunReport))
		}
		routes = append(routes, relayCandidate(relay, directDisabled))
	}
	routes = append(routes, webrtcCandidate("native-web", signal, stunReport))
	routes = append(routes, webrtcCandidate("web-web", signal, netprobe.STUNReport{}))
	markPrimaryRoutes(routes)
	return routes
}

func signalingDirectCandidate(
	signal doctorSignalReport,
	relay doctorRelayReport,
	direct doctorDirectReport,
	stunReport netprobe.STUNReport,
) routeCandidate {
	supportsDirect := hasCapability(signal.Capabilities, "direct-rendezvous-v1")
	fallback := "WebRTC DataChannel"
	if relay.Configured && relay.OK {
		fallback = "native TCP relay"
	}
	c := routeCandidate{
		Pair:      "native-native",
		Kind:      routeSignalDirect,
		Name:      "direct TCP via signaling rendezvous",
		Available: signal.OK && supportsDirect && direct.Enabled && direct.OK,
		Fallback:  fallback,
		Requires:  []string{"signaling direct rendezvous", "direct listen socket"},
	}
	if !signal.OK {
		c.Warnings = append(c.Warnings, "signaling service is not reachable")
		if signal.Error != "" {
			c.Reasons = append(c.Reasons, signal.Error)
		}
	}
	if signal.OK && !supportsDirect {
		c.Warnings = append(c.Warnings, "signaling service does not advertise direct rendezvous")
	}
	if !direct.Enabled {
		c.Warnings = append(c.Warnings, "direct TCP disabled")
	}
	if direct.Enabled && !direct.OK {
		c.Warnings = append(c.Warnings, "direct listen is not available")
		if direct.Error != "" {
			c.Reasons = append(c.Reasons, direct.Error)
		}
	}
	if !c.Available {
		return c
	}
	c.Score = 92
	c.Reasons = append(c.Reasons, "rendezvous exchanges metadata only; payload stays peer-to-peer")
	applyNativeNATHeuristic(&c, stunReport)
	if relay.Configured && relay.OK {
		c.Reasons = append(c.Reasons, "native relay fallback available")
	} else {
		c.Reasons = append(c.Reasons, "WebRTC fallback available")
	}
	applyLiveProbe(&c, "signaling", signal.LatencyMillis)
	return c
}

func inspectLAN(ctx context.Context, g *globalOptions) doctorLANReport {
	report := doctorLANReport{
		Enabled: !g.NoLAN && (g.Local || g.Relay != ""),
		Address: g.DiscoveryAddr,
		Timeout: g.LANTimeout.String(),
	}
	if !report.Enabled {
		return report
	}
	if g.LANTimeout <= 0 {
		report.Error = "LAN discovery timeout must be positive"
		return report
	}
	relays, err := discovery.DiscoverOnInterface(ctx, g.DiscoveryAddr, g.LANTimeout, g.Interface)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	report.Relays = relays
	report.OK = len(relays) > 0
	return report
}

func printDoctorLAN(out io.Writer, report doctorLANReport) {
	if !report.Enabled {
		fmt.Fprintln(out, "- LAN discovery: disabled")
		return
	}
	if report.Error != "" {
		fmt.Fprintf(out, "- LAN discovery: failed (%s)\n", report.Error)
		return
	}
	if !report.OK {
		fmt.Fprintf(out, "- LAN discovery: no relay announcements on %s within %s\n", report.Address, report.Timeout)
		return
	}
	fmt.Fprintf(out, "- LAN discovery: %d relay%s on %s\n", len(report.Relays), plural(len(report.Relays)), report.Address)
	for _, addr := range report.Relays {
		fmt.Fprintf(out, "  - relay: %s\n", addr)
	}
}

func webrtcCandidate(
	pair string,
	signal doctorSignalReport,
	stunReport netprobe.STUNReport,
) routeCandidate {
	c := routeCandidate{
		Pair:      pair,
		Kind:      routeWebRTC,
		Name:      "WebRTC DataChannel via signaling",
		Available: signal.OK,
		Fallback:  iceFallbackText(signal.ICE),
		Requires:  []string{"signaling service"},
	}
	if !signal.OK {
		c.Score = 0
		c.Warnings = append(c.Warnings, "signaling service is not reachable")
		if signal.Error != "" {
			c.Reasons = append(c.Reasons, signal.Error)
		}
		return c
	}
	c.Score = 70
	c.Reasons = append(c.Reasons, "browser-compatible route")
	switch {
	case signal.ICE.TURN > 0:
		c.Score += 15
		c.Reasons = append(c.Reasons, "TURN fallback advertised")
	case signal.ICE.STUN > 0:
		c.Score += 5
		c.Reasons = append(c.Reasons, "STUN advertised")
	default:
		c.Warnings = append(c.Warnings, "no ICE servers advertised")
	}
	if pair == "web-web" || pair == "native-web" {
		c.Score += 5
		c.Reasons = append(c.Reasons, "required whenever a browser participates")
	}
	if signal.ICE.TURN == 0 {
		c.Warnings = append(c.Warnings, "strict NAT networks may fail without TURN")
	}
	applyWebRTCNATHeuristic(&c, stunReport, signal.ICE)
	applyLiveProbe(&c, "signaling", signal.LatencyMillis)
	return c
}

func relayCandidate(relay doctorRelayReport, directDisabled bool) routeCandidate {
	c := routeCandidate{
		Pair:      "native-native",
		Kind:      routeRelayOnly,
		Name:      "native TCP relay",
		Available: relay.Configured && relay.OK,
		Requires:  []string{"native relay service"},
	}
	if directDisabled {
		c.Reasons = append(c.Reasons, "direct TCP disabled")
	}
	if !relay.Configured {
		c.Score = 0
		c.Warnings = append(c.Warnings, "relay is not configured")
		return c
	}
	if !relay.OK {
		c.Score = 0
		c.Warnings = append(c.Warnings, "relay is not reachable")
		if relay.Error != "" {
			c.Reasons = append(c.Reasons, relay.Error)
		}
		return c
	}
	c.Score = 72
	applyLiveProbe(&c, "relay", relay.LatencyMillis)
	c.Reasons = append(c.Reasons, "stable fallback for installed clients")
	c.Warnings = append(c.Warnings, "relay carries transfer bandwidth")
	if directDisabled {
		c.Score += 5
	}
	return c
}

func directRelayFallbackCandidate(
	relay doctorRelayReport,
	direct doctorDirectReport,
	stunReport netprobe.STUNReport,
) routeCandidate {
	c := routeCandidate{
		Pair:      "native-native",
		Kind:      routeDirectRelayFallback,
		Name:      "direct TCP via relay rendezvous",
		Available: relay.Configured && relay.OK && direct.Enabled && direct.OK,
		Fallback:  "native TCP relay",
		Requires:  []string{"native relay service", "direct listen socket"},
	}
	if !relay.Configured {
		c.Warnings = append(c.Warnings, "relay is required for rendezvous but is not configured")
	}
	if relay.Configured && !relay.OK {
		c.Warnings = append(c.Warnings, "relay rendezvous is not reachable")
		if relay.Error != "" {
			c.Reasons = append(c.Reasons, relay.Error)
		}
	}
	if !direct.Enabled {
		c.Warnings = append(c.Warnings, "direct TCP disabled")
	}
	if direct.Enabled && !direct.OK {
		c.Warnings = append(c.Warnings, "direct listen is not available")
		if direct.Error != "" {
			c.Reasons = append(c.Reasons, direct.Error)
		}
	}
	if !c.Available {
		c.Score = 0
		return c
	}
	c.Score = 92
	applyLiveProbe(&c, "relay rendezvous", relay.LatencyMillis)
	applyNativeNATHeuristic(&c, stunReport)
	c.Reasons = append(c.Reasons, "best native-native throughput when peers can connect directly")
	c.Reasons = append(c.Reasons, "falls back to relay if direct connect fails")
	if direct.Advertise != "" {
		c.Reasons = append(c.Reasons, "advertise "+direct.Advertise)
	}
	return c
}

func applyNativeNATHeuristic(candidate *routeCandidate, report netprobe.STUNReport) {
	if candidate == nil || !candidate.Available || !report.OK {
		return
	}
	switch report.Class {
	case netprobe.NATOpen:
		candidate.Score += 5
		candidate.Reasons = append(candidate.Reasons, "STUN indicates an openly reachable UDP mapping")
	case netprobe.NATCone:
		candidate.Score += 2
		candidate.Reasons = append(candidate.Reasons, "STUN mapping is stable across destinations")
	case netprobe.NATSymmetric:
		candidate.Score -= 12
		candidate.Warnings = append(
			candidate.Warnings,
			"UDP mapping varies by destination; keep relay or WebRTC fallback available",
		)
	case netprobe.NATUnknown:
		candidate.Reasons = append(
			candidate.Reasons,
			"STUN mapping observed but a second destination is needed for NAT classification",
		)
	}
	candidate.Score = max(1, min(100, candidate.Score))
	if report.Class != netprobe.NATUnknown {
		candidate.Reasons = append(
			candidate.Reasons,
			"UDP NAT class is a heuristic and does not guarantee TCP reachability",
		)
	}
}

func applyWebRTCNATHeuristic(
	candidate *routeCandidate,
	report netprobe.STUNReport,
	ice iceSummary,
) {
	if candidate == nil || !candidate.Available || !report.OK {
		return
	}
	switch report.Class {
	case netprobe.NATOpen:
		candidate.Score += 3
		candidate.Reasons = append(candidate.Reasons, "local UDP mapping appears openly reachable")
	case netprobe.NATCone:
		candidate.Score += 2
		candidate.Reasons = append(candidate.Reasons, "local UDP mapping is stable across STUN destinations")
	case netprobe.NATSymmetric:
		if ice.TURN > 0 {
			candidate.Score += 3
			candidate.Reasons = append(candidate.Reasons, "destination-dependent UDP mapping is covered by TURN fallback")
		} else {
			candidate.Score -= 15
			candidate.Warnings = append(
				candidate.Warnings,
				"destination-dependent UDP mapping detected; WebRTC may fail without TURN",
			)
		}
	}
	candidate.Score = max(1, min(100, candidate.Score))
}

func markPrimaryRoutes(routes []routeCandidate) {
	for i := range routes {
		routes[i].Primary = false
	}
	bestByPair := map[string]int{}
	for i, route := range routes {
		current, ok := bestByPair[route.Pair]
		if !ok || betterRoute(route, routes[current]) {
			bestByPair[route.Pair] = i
		}
	}
	for _, i := range bestByPair {
		routes[i].Primary = true
	}
}

func printDoctorHistory(out io.Writer, report doctorHistoryReport) {
	if !report.Enabled {
		fmt.Fprintln(out, "- route history: disabled")
		return
	}
	if report.Error != "" {
		fmt.Fprintf(out, "- route history: unavailable (%s)\n", report.Error)
		return
	}
	fmt.Fprintf(out, "- route history: %s\n", report.Path)
	if report.Scope.ID != "" {
		label := report.Scope.ID
		if report.Scope.Interface != "" || report.Scope.Family != "" {
			label = fmt.Sprintf("%s (%s/%s)", report.Scope.ID, report.Scope.Interface, report.Scope.Family)
		}
		fmt.Fprintf(out, "  - network scope: %s source=%s\n", label, report.Scope.Source)
	}
	if report.Legacy {
		fmt.Fprintln(out, "  - warning: using legacy device-wide history until the next observation migrates this file")
	}
	for _, kind := range []string{historyRouteDirect, historyRouteRelay, historyRouteWebRTC} {
		summary, ok := report.Routes[kind]
		if !ok || summary.Attempts == 0 {
			continue
		}
		fmt.Fprintf(
			out,
			"  - %s: %d/%d successful, consecutive failures=%d",
			kind,
			summary.Successes,
			summary.Attempts,
			summary.ConsecutiveFailures,
		)
		if summary.EWMABytesPerSecond > 0 {
			fmt.Fprintf(out, ", %.1f MB/s", summary.EWMABytesPerSecond/(1024*1024))
		}
		fmt.Fprintln(out)
		for _, path := range summary.Paths {
			fmt.Fprintf(
				out,
				"    - path %d: samples=%d, %.1f MB/s, weight=%.2f\n",
				path.Connection,
				path.Samples,
				path.EWMABytesPerSecond/(1024*1024),
				path.Weight,
			)
		}
	}
}

func betterRoute(a, b routeCandidate) bool {
	if a.Available != b.Available {
		return a.Available
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Name < b.Name
}

func doctorMatrix(routes []routeCandidate) []doctorMatrixEntry {
	order := []string{"native-native", "native-web", "web-web"}
	var matrix []doctorMatrixEntry
	for _, pair := range order {
		route, ok := primaryRoute(routes, pair)
		if !ok {
			matrix = append(matrix, doctorMatrixEntry{Pair: pair, Primary: "unavailable"})
			continue
		}
		matrix = append(matrix, doctorMatrixEntry{Pair: pair, Primary: route.Name, Fallback: route.Fallback})
	}
	return matrix
}

func primaryRoute(routes []routeCandidate, pair string) (routeCandidate, bool) {
	for _, route := range routes {
		if route.Pair == pair && route.Primary {
			return route, true
		}
	}
	return routeCandidate{}, false
}

func assessDoctorPair(report doctorReport, pair string) doctorAssessment {
	if pair == "all" || pair == "" {
		pair = "native-native"
	}
	route, found := primaryRoute(report.Routes, pair)
	usable := found && route.Available
	assessment := doctorAssessment{
		ExplanationSource: "rules",
		RouteResult:       routeResultHint(route, usable),
	}

	if !usable {
		assessment.Diagnosis = fmt.Sprintf("No usable %s transfer route was found.", pair)
		assessment.Recommendation = "fix_connectivity"
		if !report.Signal.OK {
			assessment.Recommendation = "fix_signaling"
			assessment.Actions = append(assessment.Actions,
				"Verify the signaling /api/ice and /api/health endpoints and reverse-proxy WebSocket upgrade.",
			)
		}
		if report.Relay.Configured && !report.Relay.OK {
			assessment.Actions = append(assessment.Actions,
				"Make the configured native relay reachable or remove the unusable relay configuration.",
			)
		}
		if report.Direct.Enabled && !report.Direct.OK {
			assessment.Actions = append(assessment.Actions,
				"Fix the direct TCP listen address or disable direct TCP explicitly.",
			)
		}
		return assessment
	}

	assessment.Diagnosis = fmt.Sprintf(
		"%s is the preferred %s route (score %d).",
		route.Name,
		pair,
		route.Score,
	)
	if route.Fallback != "" && route.Fallback != "none" {
		assessment.Diagnosis += " Fallback: " + route.Fallback + "."
	}

	if routeDependsOnSignaling(route) && !report.Signal.OK {
		assessment.Recommendation = "fix_signaling"
		assessment.Actions = append(assessment.Actions,
			"Verify the signaling /api/ice and /api/health endpoints and reverse-proxy WebSocket upgrade.",
		)
		return assessment
	}
	if routeUsesNativeRelay(route) && report.Relay.Configured && !report.Relay.OK {
		assessment.Recommendation = "fix_relay"
		assessment.Actions = append(assessment.Actions,
			"Make the configured native relay reachable before relying on it for rendezvous or payload fallback.",
		)
		return assessment
	}
	if report.Direct.Enabled && !report.Direct.OK && route.Kind == routeRelayOnly {
		assessment.Recommendation = "fix_direct_or_use_relay"
		assessment.Actions = append(assessment.Actions,
			"Fix the direct TCP listen address to restore peer-to-peer transfer, or keep relay-only mode explicit.",
		)
		return assessment
	}
	if route.Kind == routeWebRTC && report.Signal.ICE.TURN == 0 {
		assessment.Recommendation = "configure_turn"
		assessment.Actions = append(assessment.Actions,
			"Configure authenticated TURN before relying on browser transfers across restrictive NAT.",
		)
		return assessment
	}
	if report.STUN.OK && report.STUN.Class == netprobe.NATSymmetric &&
		strings.Contains(strings.ToLower(route.Fallback), "webrtc") && report.Signal.ICE.TURN == 0 {
		assessment.Recommendation = "configure_turn"
		assessment.Actions = append(assessment.Actions,
			"Destination-dependent NAT was detected; configure TURN so WebRTC fallback remains usable.",
		)
		return assessment
	}

	switch route.Kind {
	case routeSignalDirect:
		if strings.Contains(strings.ToLower(route.Fallback), "relay") {
			assessment.Recommendation = "direct_preferred_with_relay_fallback"
		} else {
			assessment.Recommendation = "direct_preferred_with_webrtc_fallback"
		}
	case routeDirectRelayFallback:
		assessment.Recommendation = "direct_preferred_with_relay_fallback"
	case routeRelayOnly:
		assessment.Recommendation = "relay_only"
	case routeWebRTC:
		assessment.Recommendation = "webrtc_with_turn_fallback"
	default:
		assessment.Recommendation = "use_selected_route"
	}
	return assessment
}

func advisorConfigFromEnv() advisor.Config {
	baseURL := strings.TrimSpace(os.Getenv("KIGO_AI_BASE_URL"))
	if baseURL == "" {
		baseURL = advisor.DefaultBaseURL
	}
	model := strings.TrimSpace(os.Getenv("KIGO_AI_MODEL"))
	if model == "" {
		model = advisor.DefaultModel
	}
	apiKey := strings.TrimSpace(os.Getenv("KIGO_AI_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	return advisor.Config{BaseURL: baseURL, APIKey: apiKey, Model: model}
}

func applyAIExplanation(ctx context.Context, report *doctorReport, pair string, config advisor.Config) {
	if report == nil {
		return
	}
	if pair == "all" || pair == "" {
		pair = "native-native"
	}
	route, _ := primaryRoute(report.Routes, pair)
	input := advisor.ExplainInput{
		Pair:           pair,
		Diagnosis:      report.Assessment.Diagnosis,
		Recommendation: report.Assessment.Recommendation,
		RouteKind:      string(route.Kind),
		RouteScore:     route.Score,
		Fallback:       route.Fallback,
		NATClass:       string(report.STUN.Class),
		VPNDetected:    report.Network.VPNDetected,
		STUNAvailable:  report.STUN.OK,
		TURNAvailable:  report.Signal.ICE.TURN > 0,
		Warnings:       append([]string(nil), route.Warnings...),
		Actions:        append([]string(nil), report.Assessment.Actions...),
	}
	text, err := advisor.Explain(ctx, config, input)
	if err != nil {
		report.Assessment.ExplanationSource = "rules"
		report.Assessment.ExplanationWarning = "AI explanation unavailable: " + err.Error()
		return
	}
	report.Assessment.Diagnosis = text
	report.Assessment.ExplanationSource = "ai"
	report.Assessment.ExplanationWarning = ""
}

func routeResultHint(route routeCandidate, usable bool) doctorRouteResultHint {
	if !usable {
		return doctorRouteResultHint{
			Path:   "none",
			Reason: "no_available_route",
		}
	}
	switch route.Kind {
	case routeSignalDirect:
		if strings.Contains(strings.ToLower(route.Fallback), "relay") {
			return doctorRouteResultHint{
				Path:            "direct_or_relay",
				Reason:          "direct_probe_then_relay_fallback",
				DirectAttempted: true,
			}
		}
		return doctorRouteResultHint{
			Path:            "direct_or_webrtc",
			Reason:          "direct_probe_then_webrtc_fallback",
			DirectAttempted: true,
		}
	case routeDirectRelayFallback:
		return doctorRouteResultHint{
			Path:                    "direct_or_relay",
			Reason:                  "direct_probe_then_relay_fallback",
			DirectAttempted:         true,
			RendezvousRelayRequired: true,
		}
	case routeRelayOnly:
		return doctorRouteResultHint{
			Path:                    "relay",
			Reason:                  "native_relay_selected",
			DataRelayRequired:       true,
			RendezvousRelayRequired: true,
		}
	case routeWebRTC:
		return doctorRouteResultHint{
			Path:   "webrtc",
			Reason: "webrtc_ice_selected",
		}
	default:
		return doctorRouteResultHint{Path: string(route.Kind), Reason: "selected_route"}
	}
}

func routeDependsOnSignaling(route routeCandidate) bool {
	return route.Kind == routeSignalDirect || route.Kind == routeWebRTC
}

func routeUsesNativeRelay(route routeCandidate) bool {
	return route.Kind == routeRelayOnly || route.Kind == routeDirectRelayFallback ||
		strings.Contains(strings.ToLower(route.Fallback), "native tcp relay")
}

func printDoctorAssessment(out io.Writer, assessment doctorAssessment) {
	if assessment.Diagnosis == "" {
		return
	}
	fmt.Fprintf(out, "- assessment: %s\n", assessment.Diagnosis)
	fmt.Fprintf(out, "  - explanation source: %s\n", assessment.ExplanationSource)
	if assessment.ExplanationWarning != "" {
		fmt.Fprintf(out, "  - warning: %s\n", assessment.ExplanationWarning)
	}
	fmt.Fprintf(out, "  - recommendation: %s\n", assessment.Recommendation)
	fmt.Fprintf(
		out,
		"  - route result: path=%s reason=%s direct=%t data-relay-required=%t rendezvous-relay-required=%t\n",
		assessment.RouteResult.Path,
		assessment.RouteResult.Reason,
		assessment.RouteResult.DirectAttempted,
		assessment.RouteResult.DataRelayRequired,
		assessment.RouteResult.RendezvousRelayRequired,
	)
	for _, action := range assessment.Actions {
		fmt.Fprintf(out, "  - action: %s\n", action)
	}
}

func printRouteDiagnostics(out io.Writer, matrix []doctorMatrixEntry) {
	fmt.Fprintln(out, "- route matrix:")
	for _, entry := range matrix {
		fmt.Fprintf(out, "  - %s: %s", entry.Pair, entry.Primary)
		if entry.Fallback != "" && entry.Fallback != "none" {
			if strings.Contains(entry.Fallback, " ") {
				fmt.Fprintf(out, " (%s)", entry.Fallback)
			} else {
				fmt.Fprintf(out, " fallback=%s", entry.Fallback)
			}
		}
		fmt.Fprintln(out)
	}
}

func printRouteCandidates(out io.Writer, routes []routeCandidate) {
	fmt.Fprintln(out, "- route candidates:")
	for _, route := range routes {
		status := "unavailable"
		if route.Available {
			status = "available"
		}
		primary := ""
		if route.Primary {
			primary = " primary"
		}
		fmt.Fprintf(out, "  - %s %s: %s score=%d %s%s", route.Pair, route.Kind, route.Name, route.Score, status, primary)
		if route.Fallback != "" && route.Fallback != "none" {
			fmt.Fprintf(out, " fallback=%s", route.Fallback)
		}
		fmt.Fprintln(out)
		for _, reason := range route.Reasons {
			fmt.Fprintf(out, "    - %s\n", reason)
		}
		for _, warning := range route.Warnings {
			fmt.Fprintf(out, "    - warning: %s\n", warning)
		}
	}
}

func iceFallbackText(ice iceSummary) string {
	if ice.TURN > 0 {
		return "TURN fallback available"
	}
	if ice.STUN > 0 {
		return "STUN only"
	}
	return "no ICE servers advertised"
}

func doctorRelay(ctx context.Context, addr string) error {
	report := inspectRelay(ctx, addr, "")
	printDoctorRelay(os.Stdout, report)
	if report.Configured && !report.OK {
		return errors.New(report.Error)
	}
	return nil
}

func inspectRelay(ctx context.Context, addr, proxyURL string) doctorRelayReport {
	return inspectRelayWithOptions(ctx, addr, proxyURL, nil)
}

func inspectRelayWithOptions(ctx context.Context, addr, proxyURL string, g *globalOptions) doctorRelayReport {
	report := doctorRelayReport{Addr: addr, Configured: addr != ""}
	if addr == "" {
		report.OK = true
		return report
	}
	proxyConfig, err := netproxy.Parse(proxyURL)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	var dialContext relay.DialContextFunc
	if proxyConfig != nil {
		if selectedNetworkPolicy(g) != nil {
			proxyConfig = proxyConfig.WithDialContext(netproxy.DialContextFunc(outboundDialContext(g)))
		}
		dialContext = proxyConfig.DialContext
		report.ViaProxy = true
	} else {
		dialContext = outboundDialContext(g)
	}
	started := time.Now()
	conn, err := dialContext(ctx, "tcp", addr)
	report.LatencyMillis = elapsedMillis(started)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	_ = conn.Close()
	report.OK = true
	return report
}

func printDoctorRelay(out io.Writer, report doctorRelayReport) {
	if !report.Configured {
		fmt.Fprintln(out, "- relay: not configured")
		return
	}
	if !report.OK {
		fmt.Fprintf(out, "- relay: failed (%s)\n", report.Error)
		return
	}
	via := ""
	if report.ViaProxy {
		via = " via proxy"
	}
	fmt.Fprintf(out, "- relay: ok %s%s latency=%dms\n", report.Addr, via, report.LatencyMillis)
}

func elapsedMillis(started time.Time) int64 {
	return max(time.Since(started).Milliseconds(), int64(1))
}

func applyLiveProbe(candidate *routeCandidate, kind string, latencyMillis int64) {
	if candidate == nil || latencyMillis <= 0 {
		return
	}
	candidate.Probe = &routeProbeReport{
		Kind:          kind,
		OK:            true,
		LatencyMillis: latencyMillis,
	}
	candidate.Reasons = append(candidate.Reasons, fmt.Sprintf(
		"live %s probe %dms",
		kind,
		latencyMillis,
	))
	switch {
	case latencyMillis <= 50:
		candidate.Score += 4
	case latencyMillis <= 150:
		candidate.Score += 2
	case latencyMillis > 750:
		candidate.Score -= 8
	case latencyMillis > 300:
		candidate.Score -= 4
	}
	candidate.Score = max(1, min(100, candidate.Score))
}

func doctorDirect(g *globalOptions) error {
	report := inspectDirect(context.Background(), g)
	printDoctorDirect(os.Stdout, report)
	if report.Enabled && !report.OK {
		return errors.New(report.Error)
	}
	return nil
}

func inspectDirect(ctx context.Context, g *globalOptions) doctorDirectReport {
	report := doctorDirectReport{
		Enabled:           !nativeDirectDisabled(g),
		Listen:            g.DirectListen,
		Timeout:           g.DirectTimeout.String(),
		SamePortSupported: netreuse.Supported,
		TCPProbeEnabled:   tcpPublicProbeEnabled(g),
	}
	if nativeDirectDisabled(g) {
		if strings.TrimSpace(g.Proxy) != "" {
			report.DisabledReason = "--proxy"
		} else {
			report.DisabledReason = "--no-direct"
		}
		report.OK = true
		return report
	}
	ln, err := listenDirect(ctx, directListenAddress(g))
	if err != nil {
		report.Error = err.Error()
		return report
	}
	defer ln.Close()
	report.OK = true
	report.Listen = ln.Addr().String()
	report.Advertise = advertisedDirectAddr(g, ln.Addr())
	if report.TCPProbeEnabled {
		candidate, err := probeRelayObservedDirectCandidateResult(
			ctx,
			g,
			"doctor-direct-probe",
			"sender",
			ln,
		)
		if err != nil {
			report.PublicProbeError = err.Error()
		} else {
			report.PublicAddress = candidate.Address
		}
	}
	return report
}

func printDoctorDirect(out io.Writer, report doctorDirectReport) {
	if !report.Enabled {
		reason := report.DisabledReason
		if reason == "" {
			reason = "configuration"
		}
		fmt.Fprintf(out, "- direct: disabled by %s\n", reason)
		return
	}
	if !report.OK {
		fmt.Fprintf(out, "- direct: failed (%s)\n", report.Error)
		return
	}
	samePort := "no"
	if report.SamePortSupported {
		samePort = "yes"
	}
	fmt.Fprintf(
		out,
		"- direct: ok listen=%s advertise=%s timeout=%s same-port=%s\n",
		report.Listen,
		report.Advertise,
		report.Timeout,
		samePort,
	)
	if report.PublicAddress != "" {
		fmt.Fprintf(out, "  - relay-observed TCP mapping: %s\n", report.PublicAddress)
	} else if report.TCPProbeEnabled && report.PublicProbeError != "" {
		fmt.Fprintf(out, "  - relay-observed TCP mapping unavailable: %s\n", report.PublicProbeError)
	}
}

func apiURL(base, path string) (string, error) {
	if base == "" {
		return "", errors.New("empty base URL")
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func withInterrupt(parent context.Context) context.Context {
	ctx, _ := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	return ctx
}
