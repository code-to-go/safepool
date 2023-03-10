package invite

import (
	"bytes"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/code-to-go/safepool/apps/common"
	"github.com/code-to-go/safepool/core"
	"github.com/code-to-go/safepool/pool"
	"github.com/code-to-go/safepool/security"
	"github.com/godruoyi/go-snowflake"
)

var ErrInvalidToken = errors.New("provided token is invalid: missing name or configs")

type Invite struct {
	Subject      string            `json:"subject"`
	Sender       security.Identity `json:"sender"`
	RecipientIds []string          `json:"recipientIds"`
	Config       *pool.Config      `json:"config"`
}

func (i Invite) Join() error {
	if i.Config == nil {
		return core.ErrNotAuthorized
	}

	c := i.Config
	if c.Name == "" || (len(c.Public)+len(c.Private)) == 0 {
		core.IsErr(ErrInvalidToken, "invalid config '%v': %v", c)
		return ErrInvalidToken
	} else {
		core.Info("valid token for pool '%s'", c.Name)
	}

	err := security.SetIdentity(i.Sender)
	if core.IsErr(err, "cannot save identity '%s': %v", i.Sender.Nick) {
		return err
	}

	err = security.Trust(i.Sender, true)
	if core.IsErr(err, "cannot trust identity '%s': %v", i.Sender.Nick) {
		return err
	}

	return pool.Define(*c)
}

func Add(p *pool.Pool, i Invite) error {
	bs, err := json.Marshal(i)
	if core.IsErr(err, "cannot marshal invite: %v") {
		return err
	}
	name := fmt.Sprintf("invite/%d", snowflake.ID())
	_, err = p.Send(name, core.NewBytesReader(bs), int64(len(bs)), nil)
	core.IsErr(err, "cannot send invite to pool '%s': %v", p.Name)
	return err
}

func Receive(p *pool.Pool, after int64, onlyMine bool) ([]Invite, error) {
	p.Sync()
	ctime := common.GetBreakpoint(p.Name, "invite")
	fs, _ := p.List(ctime)
	for _, f := range fs {
		accept(p, f)
		ctime = f.CTime
	}
	common.SetBreakpoint(p.Name, "invite", ctime)
	return sqlGetInvites(p.Name, after, onlyMine)
}

func accept(p *pool.Pool, f pool.Head) {
	if !strings.HasPrefix(f.Name, "invite/") {
		return
	}

	var buf bytes.Buffer
	err := p.Receive(f.Id, nil, &buf)
	if core.IsErr(err, "cannot retrieve invite from '%s/%d': %v", p.Name, f.Id) {
		return
	}

	var i Invite
	err = json.Unmarshal(buf.Bytes(), &i)
	if core.IsErr(err, "cannot unmarshal invite: %v") {
		return
	}

	err = sqlSetInvite(p.Name, f.CTime, i)
	core.IsErr(err, "cannot save document to db: %v")
}

type Token struct {
	Subject    string            `json:"s"`
	SenderId   string            `json:"e"`
	SenderNick string            `json:"d"`
	Noonce     []byte            `json:"n"`
	Keys       map[string][]byte `json:"k"`
	Config     []byte            `json:"c"`
}

func Decode(self security.Identity, token string) (Invite, error) {
	token = strings.ReplaceAll(token, "_", "/")
	parts := strings.Split(token, ":")
	if len(parts) != 2 {
		return Invite{}, ErrInvalidToken
	}

	tk64, sig64 := parts[0], parts[1]
	tk, _ := base64.StdEncoding.DecodeString(tk64)
	sig, _ := base64.StdEncoding.DecodeString(sig64)

	var t Token
	err := json.Unmarshal(tk, &t)
	if core.IsErr(err, "invalid json format for '%s': %v", tk) {
		return Invite{}, ErrInvalidToken
	}

	if !security.Verify(t.SenderId, tk, sig) {
		core.IsErr(security.ErrInvalidSignature, "token has invalid signature: %v")
		return Invite{}, security.ErrInvalidSignature
	}

	sender, ok, _ := security.GetIdentity(t.SenderId)
	if !ok {
		sender, err = security.IdentityFromId(t.SenderId)
		if core.IsErr(err, "cannot create identity from Id: %v") {
			return Invite{}, err
		}
		sender.Nick = t.SenderNick
	}
	i := Invite{
		Subject: t.Subject,
		Sender:  sender,
	}

	var masterKey []byte
	selfId := self.Id()
	for id, key := range t.Keys {
		i.RecipientIds = append(i.RecipientIds, id)
		if id == selfId {
			masterKey, err = security.EcDecrypt(self, key)
			if core.IsErr(err, "cannot decrypt master key from '%s': %v", key) {
				return Invite{}, err
			}
		}
	}
	if masterKey != nil {
		t.Config, err = security.DecryptBlock(masterKey, t.Noonce, t.Config)
		if core.IsErr(err, "cannot decrypt token '%v' with found masterKey: %v", t.Config) {
			return i, err
		}
		var c pool.Config
		err = json.Unmarshal(t.Config, &c)
		if core.IsErr(err, "cannot unmarshal config inside token '%v': %v", t.Config) {
			return i, err
		}
		i.Config = &c
	}

	return i, nil
}

func Encode(i Invite) (string, error) {
	c, err := json.Marshal(i.Config)
	if core.IsErr(err, "cannot marshal config to token: %v") {
		return "", err
	}

	t := Token{
		Subject:    i.Subject,
		SenderId:   i.Sender.Id(),
		SenderNick: i.Sender.Nick,
		Keys:       map[string][]byte{},
		Config:     c,
	}
	if len(i.RecipientIds) > 0 {
		t.Noonce = security.GenerateBytesKey(aes.BlockSize)
		masterKey := security.GenerateBytesKey(32)
		t.Config, err = security.EncryptBlock(masterKey, t.Noonce, c)
		if core.IsErr(err, "cannot encrypt token: %v") {
			return "", err
		}
		for _, id := range i.RecipientIds {
			identity, err := security.IdentityFromId(id)
			if err == nil {
				key, err := security.EcEncrypt(identity, masterKey)
				if err == nil {
					t.Keys[id] = key
				}
			}
		}
	}

	tk, err := json.Marshal(t)
	if core.IsErr(err, "cannot marshal token: %v") {
		return "", err
	}
	sig, err := security.Sign(i.Sender, tk)
	if core.IsErr(err, "cannot sign with host key: %v") {
		return "", err
	}

	return strings.ReplaceAll(fmt.Sprintf("%s:%s",
		base64.StdEncoding.EncodeToString(tk),
		base64.StdEncoding.EncodeToString(sig)), "/", "_"), nil

}
