package matrix

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type Client struct {
	client       *mautrix.Client
	allowedRooms map[string]struct{}
	joinedRooms  map[string]struct{}
	mu           sync.RWMutex
	logger       *log.Logger
}

func NewClient(homeserverURL, userID, accessToken string, allowedRooms []string, logger *log.Logger) (*Client, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if strings.TrimSpace(homeserverURL) == "" {
		return nil, errors.New("homeserver URL is required")
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("access token is required")
	}
	trimmedUserID := strings.TrimSpace(userID)
	if trimmedUserID == "" {
		return nil, errors.New("matrix user ID is required")
	}
	if !strings.HasPrefix(trimmedUserID, "@") || !strings.Contains(trimmedUserID, ":") {
		return nil, errors.New("matrix user ID must look like @user:domain")
	}

	cli, err := mautrix.NewClient(homeserverURL, id.UserID(trimmedUserID), accessToken)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]struct{}, len(allowedRooms))
	for _, roomID := range allowedRooms {
		trimmed := strings.TrimSpace(roomID)
		if trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}

	c := &Client{
		client:       cli,
		allowedRooms: allowed,
		joinedRooms:  make(map[string]struct{}),
		logger:       logger,
	}

	syncer := cli.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.StateMember, c.handleMemberEvent)

	return c, nil
}

func (c *Client) StartSync(ctx context.Context) error {
	if err := c.loadJoinedRooms(ctx); err != nil {
		return err
	}
	return c.client.SyncWithContext(ctx)
}

func (c *Client) SendMessage(ctx context.Context, roomID, body, format string) error {
	if roomID == "" {
		return errors.New("room ID is required")
	}
	if err := c.ensureJoined(ctx, roomID); err != nil {
		return err
	}

	content := event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    body,
	}
	if format == "html" {
		content.Format = event.FormatHTML
		content.FormattedBody = body
	} else if format == "markdown" {
		content.Format = "org.matrix.custom.markdown"
		content.FormattedBody = body
	}

	_, err := c.client.SendMessageEvent(ctx, id.RoomID(roomID), event.EventMessage, content)
	return err
}

func (c *Client) handleMemberEvent(ctx context.Context, evt *event.Event) {
	if evt.StateKey == nil || c.client.UserID == "" {
		return
	}
	if *evt.StateKey != c.client.UserID.String() {
		return
	}

	if evt.Content.Parsed == nil {
		if err := evt.Content.ParseRaw(event.StateMember); err != nil {
			c.logger.Printf("matrix: failed to parse membership event: %v", err)
			return
		}
	}
	content, ok := evt.Content.Parsed.(*event.MemberEventContent)
	if !ok {
		c.logger.Printf("matrix: unexpected membership content type")
		return
	}
	if content.Membership != event.MembershipInvite {
		return
	}

	roomID := evt.RoomID.String()
	if !c.isAllowed(roomID) {
		c.logger.Printf("matrix: ignoring invite to room %s", roomID)
		return
	}
	if _, err := c.client.JoinRoom(ctx, roomID, nil); err != nil {
		c.logger.Printf("matrix: join room failed for %s: %v", roomID, err)
		return
	}

	c.setJoined(roomID)
}

func (c *Client) ensureJoined(ctx context.Context, roomID string) error {
	if c.isJoined(roomID) {
		return nil
	}
	if !c.isAllowed(roomID) {
		return errors.New("room is not allow-listed")
	}
	if _, err := c.client.JoinRoom(ctx, roomID, nil); err != nil {
		return err
	}
	c.setJoined(roomID)
	return nil
}

func (c *Client) loadJoinedRooms(ctx context.Context) error {
	joined, err := c.client.JoinedRooms(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, roomID := range joined.JoinedRooms {
		c.joinedRooms[roomID.String()] = struct{}{}
	}
	return nil
}

func (c *Client) isAllowed(roomID string) bool {
	if len(c.allowedRooms) == 0 {
		return false
	}
	_, ok := c.allowedRooms[roomID]
	return ok
}

func (c *Client) isJoined(roomID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.joinedRooms[roomID]
	return ok
}

func (c *Client) setJoined(roomID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.joinedRooms[roomID] = struct{}{}
}
