package notify

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// unsubscribeAD binds unsubscribe tokens to their purpose.
const unsubscribeAD = "notification-unsubscribe"

type unsubToken struct {
	U uuid.UUID `json:"u"`
	T string    `json:"t"` // event type; "" = every email notification
}

// UnsubscribeToken returns a signed (authenticated-encrypted) token that turns
// off emails of type t for user uid ("" turns off email entirely).
func (s *Service) UnsubscribeToken(uid uuid.UUID, t string) (string, error) {
	b, _ := json.Marshal(unsubToken{U: uid, T: t})
	return s.tokenBox.Seal(b, unsubscribeAD)
}

var errBadToken = errors.New("invalid unsubscribe token")

func (s *Service) parseUnsubscribe(tok string) (unsubToken, error) {
	var t unsubToken
	b, err := s.tokenBox.Open(tok, unsubscribeAD)
	if err != nil || json.Unmarshal(b, &t) != nil || t.U == uuid.Nil {
		return t, errBadToken
	}
	if t.T != "" {
		if _, ok := Meta(t.T); !ok {
			return t, errBadToken
		}
	}
	return t, nil
}
