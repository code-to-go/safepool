package pool

import (
	"fmt"
	"path"
	"strings"

	"github.com/code-to-go/safepool/core"
)

func (p *Pool) Sub(sub string, ids []string, apps []string) (Config, error) {
	parent, name := path.Split(p.Name)
	if strings.HasPrefix(name, "#") {
		name = path.Join(parent, fmt.Sprintf("#%s", sub))
	} else {
		name = path.Join(p.Name, fmt.Sprintf("#%s", sub))
	}

	c := Config{
		Name:    name,
		Public:  p.config.Public,
		Private: p.config.Private,
	}

	err := Define(c)
	if core.IsErr(err, "cannot define Forked pool %s: %v", name) {
		return Config{}, err
	}

	p2, err := Create(p.Self, name, apps)
	if core.IsErr(err, "cannot create Forked pool %s: %v", name) {
		return Config{}, err
	}
	defer p2.Close()

	for _, id := range ids {
		p2.SetAccess(id, Active)
	}

	return c, nil
}
