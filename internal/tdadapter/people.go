package tdadapter

import (
	"context"
	"errors"

	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

func (a *Adapter) user(ctx context.Context, id int64) (userWire, error) {
	if id <= 0 {
		return userWire{}, ErrInvalidResponse
	}
	if u, ok := a.index.user(id); ok {
		return u, nil
	}
	var u userWire
	err := a.read(ctx, "getUser", map[string]any{"user_id": id}, "user", &u)
	if err != nil {
		return u, err
	}
	if u.ID != id {
		return userWire{}, ErrInvalidResponse
	}
	return u, nil
}

// The current worker contract asks for the complete contact snapshot. Hard cap
// prevents unbounded results; normal TDLib ordering supplies updateUser first.
func (a *Adapter) ListContacts(ctx context.Context) ([]telegram.Contact, error) {
	var r struct {
		UserIDs []int64 `json:"user_ids"`
	}
	if err := a.read(ctx, "getContacts", nil, "users", &r); err != nil {
		return nil, err
	}
	if r.UserIDs == nil || len(r.UserIDs) > maxIndexEntries {
		return nil, ErrInvalidResponse
	}
	out := make([]telegram.Contact, 0, len(r.UserIDs))
	seen := map[int64]bool{}
	for _, id := range r.UserIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		u, err := a.user(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, telegram.Contact{TelegramUserID: id, FirstName: u.FirstName, LastName: u.LastName, Username: u.username(), IsMutual: u.Mutual})
	}
	if err := a.authorized(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

type memberWire struct {
	Sender struct {
		Type   string `json:"@type"`
		UserID int64  `json:"user_id"`
		ChatID int64  `json:"chat_id"`
	} `json:"member_id"`
	Status struct {
		Type     string `json:"@type"`
		IsMember bool   `json:"is_member"`
	} `json:"status"`
}

func (a *Adapter) ListMembers(ctx context.Context, chatID int64, raw string, limit int) (telegram.MemberPage, error) {
	if limit < 1 || limit > 200 {
		return telegram.MemberPage{}, errors.New("invalid member page size")
	}
	cursor, err := a.cursor(raw, "members", chatID)
	if err != nil {
		return telegram.MemberPage{}, err
	}
	c, err := a.getChat(ctx, chatID)
	if err != nil {
		return telegram.MemberPage{}, err
	}
	var r struct {
		Members []memberWire `json:"members"`
	}
	switch c.Type.Type {
	case "chatTypeSupergroup":
		if c.Type.SupergroupID <= 0 {
			return telegram.MemberPage{}, ErrInvalidResponse
		}
		err = a.read(ctx, "getSupergroupMembers", map[string]any{"supergroup_id": c.Type.SupergroupID, "filter": nil, "offset": cursor.Offset, "limit": limit}, "chatMembers", &r)
		if err == nil && (r.Members == nil || len(r.Members) > limit) {
			return telegram.MemberPage{}, ErrInvalidResponse
		}
	case "chatTypeBasicGroup":
		if c.Type.BasicGroupID <= 0 {
			return telegram.MemberPage{}, ErrInvalidResponse
		}
		err = a.read(ctx, "getBasicGroupFullInfo", map[string]any{"basic_group_id": c.Type.BasicGroupID}, "basicGroupFullInfo", &r)
		if err == nil {
			if r.Members == nil || len(r.Members) > maxIndexEntries {
				return telegram.MemberPage{}, ErrInvalidResponse
			}
			if cursor.Offset >= len(r.Members) {
				r.Members = nil
			} else {
				end := cursor.Offset + limit
				if end > len(r.Members) {
					end = len(r.Members)
				}
				r.Members = r.Members[cursor.Offset:end]
			}
		}
	default:
		return telegram.MemberPage{}, ErrExcluded
	}
	if err != nil {
		return telegram.MemberPage{}, err
	}
	page := telegram.MemberPage{Items: []telegram.Member{}}
	for _, m := range r.Members {
		role := "member"
		switch m.Status.Type {
		case "chatMemberStatusCreator":
			if !m.Status.IsMember {
				continue
			}
			role = "creator"
		case "chatMemberStatusAdministrator":
			role = "administrator"
		case "chatMemberStatusMember":
		case "chatMemberStatusRestricted":
			if !m.Status.IsMember {
				continue
			}
			role = "restricted"
		case "chatMemberStatusLeft", "chatMemberStatusBanned":
			continue
		default:
			return telegram.MemberPage{}, ErrInvalidResponse
		}
		member := telegram.Member{Role: role}
		switch m.Sender.Type {
		case "messageSenderUser":
			if m.Sender.UserID <= 0 {
				return telegram.MemberPage{}, ErrInvalidResponse
			}
			member.PeerType = "user"
			member.TelegramPeerID = m.Sender.UserID
			// Missing display data does not trigger hundreds of native lookups. A later
			// updateUser/snapshot can enrich names; identity and role are still accurate.
			if u, ok := a.index.user(m.Sender.UserID); ok {
				member.FirstName = u.FirstName
				member.LastName = u.LastName
				member.Username = u.username()
			}
		case "messageSenderChat":
			if m.Sender.ChatID == 0 {
				return telegram.MemberPage{}, ErrInvalidResponse
			}
			member.PeerType = "chat"
			member.TelegramPeerID = m.Sender.ChatID
		default:
			return telegram.MemberPage{}, ErrInvalidResponse
		}
		page.Items = append(page.Items, member)
	}
	if len(r.Members) > 0 {
		cursor.Offset += len(r.Members)
		if cursor.Offset > maxIndexEntries {
			return telegram.MemberPage{}, errors.New("member scan capacity reached")
		}
		page.NextCursor = encodeCursor(cursor)
	}
	if err := a.authorized(ctx); err != nil {
		return telegram.MemberPage{}, err
	}
	return page, nil
}
