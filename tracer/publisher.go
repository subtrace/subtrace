package tracer

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"nhooyr.io/websocket"
	"subtrace.dev/pubsub"
	"subtrace.dev/rpc"
)

var defaultPublisher *publisher

type publisher struct {
	ch chan []byte
}

func InitPublisher(ctx context.Context) {
	defaultPublisher = &publisher{ch: make(chan []byte, 4096)}
	go defaultPublisher.loop(ctx)
}

func (p *publisher) dialWebSocket(ctx context.Context) (*websocket.Conn, error) {
	var pub pubsub.JoinPublisher_Response
	if code, err := rpc.Call(ctx, &pub, "/api/JoinPublisher", &pubsub.JoinPublisher_Request{}); err != nil {
		return nil, fmt.Errorf("call JoinPublisher: %w", err)
	} else if code != http.StatusOK || (pub.Error != nil && *pub.Error != "") {
		err := fmt.Errorf("JoinPublisher: %s", http.StatusText(code))
		if pub.Error != nil && *pub.Error != "" {
			err = fmt.Errorf("%w: %s", err, *pub.Error)
		}
		return nil, err
	}

	conn, resp, err := websocket.Dial(ctx, pub.WebsocketUrl, &websocket.DialOptions{
		HTTPClient: http.DefaultClient,
		HTTPHeader: rpc.GetHeader(),
	})
	if err != nil {
		defer resp.Body.Close()
		return nil, fmt.Errorf("dial: %w", err)
	}

	return conn, nil
}

func (p *publisher) loop(ctx context.Context) {
	var conn *websocket.Conn
	defer func() {
		if conn != nil {
			conn.CloseNow()
		}
	}()

	for {
		select {
		case b := <-p.ch:
			var backoff time.Duration
			for {
				if backoff > 0 {
					time.Sleep(backoff)
				}

				if conn == nil {
					c, err := p.dialWebSocket(ctx)
					if err != nil {
						slog.Error("failed to initialize websocket, waiting and retrying", "err", err, "wait", backoff)
						backoff = min(backoff+2*time.Second, 30*time.Second)
						continue
					}

					conn = c
				}

				backoff = 0

				if err := conn.Write(ctx, websocket.MessageBinary, b); err != nil {
					slog.Error("failed to write to websocket, waiting and retrying with a new websocket", "err", err, "wait", backoff)
					conn.CloseNow()
					conn = nil
				} else {
					break
				}
			}

		case <-ctx.Done():
			return
		}
	}
}
