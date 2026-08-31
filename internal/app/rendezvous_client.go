package app

import (
	"context"
	"fmt"
	"net/url"

	"github.com/suir1/kigo/internal/transport"
)

func exchangeRendezvousJSON(
	ctx context.Context,
	g *globalOptions,
	endpoint string,
	kind string,
	request any,
	response any,
) error {
	conn, httpResponse, err := outboundWebSocketDialer(g).DialContext(ctx, endpoint, nil)
	if httpResponse != nil && httpResponse.Body != nil {
		defer httpResponse.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", kind, err)
	}
	defer conn.Close()
	stopContextWatch := transport.CloseOnContextDone(ctx, conn)
	defer stopContextWatch()
	if err := conn.WriteJSON(request); err != nil {
		return fmt.Errorf("send %s: %w", kind, err)
	}
	if err := conn.ReadJSON(response); err != nil {
		return fmt.Errorf("read %s: %w", kind, err)
	}
	return nil
}

func rendezvousURL(base, pathPrefix, roomToken, role string) (string, error) {
	httpURL, err := apiURL(base, pathPrefix+roomToken)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(httpURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("signaling URL must use http or https: %q", base)
	}
	query := u.Query()
	query.Set("role", role)
	u.RawQuery = query.Encode()
	return u.String(), nil
}
