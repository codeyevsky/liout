package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/codeyevsky/liout/internal/model"
)

// Send modes, selectable from config when LinkedIn changes its endpoints.
const (
	ModeMessenger = "messenger" // current: voyagerMessagingDashMessengerMessages
	ModeLegacy    = "legacy"    // legacy: messaging/conversations?action=create
	ModeAuto      = "auto"      // try messenger, fall back to legacy
)

// SendMessage sends a 1:1 message; p.URN is resolved from the publicIdentifier when empty.
func (c *Client) SendMessage(ctx context.Context, p *model.Person, text, mode string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty message")
	}
	if p.URN == "" {
		urn, err := c.ResolveURN(ctx, p.ProfileID)
		if err != nil {
			return err
		}
		p.URN = urn
	}
	switch mode {
	case ModeLegacy:
		return c.sendLegacy(ctx, p.URN, text)
	case ModeMessenger:
		return c.sendMessenger(ctx, p.URN, text)
	default:
		err := c.sendMessenger(ctx, p.URN, text)
		if err == nil {
			return nil
		}
		if isFatal(err) {
			return err
		}
		return c.sendLegacy(ctx, p.URN, text)
	}
}

func isFatal(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "session invalid") || strings.Contains(err.Error(), "429"))
}

func (c *Client) sendMessenger(ctx context.Context, recipientID, text string) error {
	me, err := c.Me(ctx)
	if err != nil {
		return err
	}
	body := map[string]any{
		"message": map[string]any{
			"body":                map[string]any{"text": text, "attributes": []any{}},
			"renderContentUnions": []any{},
		},
		"mailboxUrn":                   "urn:li:fsd_profile:" + me,
		"trackingId":                   randomToken(),
		"dedupeByClientGeneratedToken": false,
		"hostRecipientUrns":            []string{"urn:li:fsd_profile:" + recipientID},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = c.Do(ctx, http.MethodPost, "/voyager/api/voyagerMessagingDashMessengerMessages?action=createMessage", b)
	if err != nil {
		return fmt.Errorf("messenger: %w", err)
	}
	return nil
}

func (c *Client) sendLegacy(ctx context.Context, recipientID, text string) error {
	body := map[string]any{
		"keyVersion": "LEGACY_INBOX",
		"conversationCreate": map[string]any{
			"eventCreate": map[string]any{
				"originToken": uuid4(),
				"value": map[string]any{
					"com.linkedin.voyager.messaging.create.MessageCreate": map[string]any{
						"attributedBody": map[string]any{"text": text, "attributes": []any{}},
						"attachments":    []any{},
					},
				},
			},
			"recipients": []string{recipientID},
			"subtype":    "MEMBER_TO_MEMBER",
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = c.Do(ctx, http.MethodPost, "/voyager/api/messaging/conversations?action=create", b)
	if err != nil {
		return fmt.Errorf("legacy: %w", err)
	}
	return nil
}

// SendInvite sends a connection request with a note (300 character limit).
func (c *Client) SendInvite(ctx context.Context, p *model.Person, note string) error {
	if p.URN == "" {
		urn, err := c.ResolveURN(ctx, p.ProfileID)
		if err != nil {
			return err
		}
		p.URN = urn
	}
	if r := []rune(note); len(r) > 300 {
		return fmt.Errorf("invitation note is %d characters, limit is 300", len(r))
	}
	body := map[string]any{
		"invitee": map[string]any{
			"com.linkedin.voyager.growth.invitation.InviteeProfile": map[string]any{
				"profileId": p.URN,
			},
		},
		"trackingId": randomToken(),
	}
	if note != "" {
		body["message"] = note
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = c.Do(ctx, http.MethodPost, "/voyager/api/growth/normInvitations", b)
	return err
}
