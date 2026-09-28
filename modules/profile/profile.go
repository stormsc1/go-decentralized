// Package profile keeps people's profiles: a name, an avatar and a bio, the
// same in every service. A person signs their profile with a device key;
// the node they set it on is their home: it keeps the profile, announces
// their DID in the DHT and serves the profile to any node that asks. Other
// nodes fetch it from there, check the signature and keep a copy for a
// while. See docs/design/profiles.md.
package profile

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"go-decentralized/did"
	"go-decentralized/module"
)

const Name = "profile"

// SetPurpose is what a person signs their profile for.
const SetPurpose = "profile.set"

//go:embed module.yaml
var manifest []byte

const (
	maxAvatar    = 256 << 10
	defaultFresh = 10 * time.Minute
	fetchTimeout = 10 * time.Second
	skew         = 5 * time.Minute // how far ahead a profile may be dated
)

var avatarKey = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Config struct {
	// Fresh is how long a fetched profile is served before its home is
	// asked again.
	Fresh time.Duration `yaml:"fresh"`
}

type Module struct {
	env module.Env
	cfg Config
}

func New(decode func(any) error, env module.Env) (module.Module, error) {
	m := &Module{env: env}
	if err := decode(&m.cfg); err != nil {
		return nil, err
	}
	if m.cfg.Fresh <= 0 {
		m.cfg.Fresh = defaultFresh
	}
	return m, nil
}

func (m *Module) Manifest() module.Manifest { return module.MustParseManifest(manifest) }

func (m *Module) Handlers() map[string]module.Handler {
	return map[string]module.Handler{
		"set":          module.HandlerFor(m.set),
		"set_avatar":   module.HandlerFor(m.setAvatar),
		"get":          module.HandlerFor(m.get),
		"lookup":       module.HandlerFor(m.lookup),
		"avatar":       module.HandlerFor(m.avatar),
		"fetch":        module.HandlerFor(m.fetch),
		"fetch_avatar": module.HandlerFor(m.fetchAvatar),
	}
}

// Run announces the people whose home this node is, so their DIDs resolve
// to it; routing keeps announcing them.
func (m *Module) Run(ctx context.Context) {
	records, err := m.env.Entities("profile").Query(ctx, module.Query{Where: map[string]any{"node": m.env.NodeID}, Limit: 1000})
	if err != nil {
		slog.Warn("profile: can't list the people here", "err", err)
		return
	}
	for _, r := range records {
		m.provide(ctx, r.ID)
	}
}

// provide announces this node as the home of the person id.
func (m *Module) provide(ctx context.Context, id string) {
	if err := m.env.Call(ctx, "routing.provide", map[string]string{"key": id}, nil); err != nil {
		slog.Warn("profile: can't announce a person", "id", id, "err", err)
	}
}

// Profile is a person's profile as a node keeps it: what they signed, and
// where it's from.
type Profile struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Bio    string `json:"bio,omitempty"`
	Avatar string `json:"avatar,omitempty"` // the SHA-256 of the image, in hex
	// Time is when the person signed it: newer replaces older.
	Time time.Time `json:"time"`
	// Node is the person's home node.
	Node string `json:"node"`
	// Signed is the person's signature over {name, bio, avatar, time}, for
	// anyone to check.
	Signed did.Signed `json:"signed"`
	// Fetched is when this node last got the profile from its home, if this
	// node isn't it.
	Fetched time.Time `json:"fetched,omitzero"`
}

// claim is what a person signs.
type claim struct {
	Name   string    `json:"name"`
	Bio    string    `json:"bio,omitempty"`
	Avatar string    `json:"avatar,omitempty"`
	Time   time.Time `json:"time"`
}

func notFound(err error) bool { return module.Code(err) == module.CodeNotFound }

func signedIn(ctx context.Context) (string, error) {
	user := module.User(ctx)
	if user == "" {
		return "", module.Errorf(module.CodePermissionDenied, "sign in first")
	}
	return user, nil
}

// verify checks a signed profile and returns it, without its node.
func verify(s did.Signed) (Profile, error) {
	id, err := s.Verify(SetPurpose, time.Now())
	if err != nil {
		return Profile{}, module.Errorf(module.CodeInvalidArgument, "%v", err)
	}
	var c claim
	if err := json.Unmarshal(s.Data, &c); err != nil {
		return Profile{}, module.Errorf(module.CodeInvalidArgument, "a profile is {name, bio, avatar, time}: %v", err)
	}
	c.Name = strings.TrimSpace(c.Name)
	switch {
	case c.Name == "" || len(c.Name) > 80:
		return Profile{}, module.Errorf(module.CodeInvalidArgument, "a name is 1 to 80 characters")
	case len(c.Bio) > 500:
		return Profile{}, module.Errorf(module.CodeInvalidArgument, "a bio is at most 500 characters")
	case c.Avatar != "" && !avatarKey.MatchString(c.Avatar):
		return Profile{}, module.Errorf(module.CodeInvalidArgument, "an avatar is the SHA-256 of the image, in hex")
	case c.Time.IsZero() || c.Time.After(time.Now().Add(skew)):
		return Profile{}, module.Errorf(module.CodeInvalidArgument, "a profile is dated when it was signed")
	}
	return Profile{ID: id, Name: c.Name, Bio: c.Bio, Avatar: c.Avatar, Time: c.Time.UTC(), Signed: s}, nil
}

func (m *Module) set(ctx context.Context, in struct {
	Signed did.Signed `json:"signed"`
}) (Profile, error) {
	user, err := signedIn(ctx)
	if err != nil {
		return Profile{}, err
	}
	p, err := verify(in.Signed)
	if err != nil {
		return Profile{}, err
	}
	if p.ID != user {
		return Profile{}, module.Errorf(module.CodePermissionDenied, "the profile is %s's, but %s is signed in", p.ID, user)
	}
	if p.Avatar != "" {
		if _, err := m.env.Blobs().Stat(ctx, p.Avatar); notFound(err) {
			return Profile{}, module.Errorf(module.CodeInvalidArgument, "no avatar %s here: set_avatar first", p.Avatar)
		} else if err != nil {
			return Profile{}, err
		}
	}
	var have Profile
	version, err := m.env.Entities("profile").Get(ctx, p.ID, &have)
	if err != nil && !notFound(err) {
		return Profile{}, err
	}
	if err == nil && !p.Time.After(have.Time) {
		return Profile{}, module.Errorf(module.CodeInvalidArgument, "the profile kept is from %s, this one is older", have.Time.Format(time.RFC3339))
	}
	p.Node = m.env.NodeID
	if _, err := m.env.Entities("profile").PutIf(ctx, p.ID, p, version); err != nil {
		return Profile{}, err
	}
	m.provide(ctx, p.ID)
	return p, m.env.Emit(ctx, "updated", map[string]string{"id": p.ID})
}

func (m *Module) setAvatar(ctx context.Context, in struct {
	Data        []byte `json:"data"`
	ContentType string `json:"content_type"`
}) (map[string]string, error) {
	if _, err := signedIn(ctx); err != nil {
		return nil, err
	}
	if len(in.Data) == 0 || len(in.Data) > maxAvatar {
		return nil, module.Errorf(module.CodeInvalidArgument, "an avatar is 1 byte to %d KiB", maxAvatar>>10)
	}
	sum := sha256.Sum256(in.Data)
	key := hex.EncodeToString(sum[:])
	if err := m.env.Blobs().Put(ctx, key, in.Data, in.ContentType); err != nil {
		return nil, err
	}
	return map[string]string{"avatar": key}, nil
}

type person struct {
	ID   string `json:"id"`
	Node string `json:"node"`
}

func (m *Module) get(ctx context.Context, in person) (Profile, error) {
	return m.resolve(ctx, in.ID, in.Node)
}

func (m *Module) lookup(ctx context.Context, in struct {
	People []person `json:"people"`
}) (map[string][]Profile, error) {
	found := make([]Profile, len(in.People))
	ok := make([]bool, len(in.People))
	var wg sync.WaitGroup
	for i, p := range in.People {
		wg.Go(func() {
			profile, err := m.resolve(ctx, p.ID, p.Node)
			found[i], ok[i] = profile, err == nil
		})
	}
	wg.Wait()
	profiles := []Profile{}
	for i := range found {
		if ok[i] {
			profiles = append(profiles, found[i])
		}
	}
	return map[string][]Profile{"profiles": profiles}, nil
}

// resolve returns the profile of the person id: this node's own copy if it's
// their home or the copy is fresh, else one fetched from their home, found
// through hint, the copy's node or the DHT. A stale copy is better than
// nothing.
func (m *Module) resolve(ctx context.Context, id, hint string) (Profile, error) {
	var have Profile
	version, err := m.env.Entities("profile").Get(ctx, id, &have)
	known := err == nil
	if err != nil && !notFound(err) {
		return Profile{}, err
	}
	if known && (have.Node == m.env.NodeID || time.Since(have.Fetched) < m.cfg.Fresh) {
		return have, nil
	}
	var nodes []string
	if hint != "" {
		nodes = append(nodes, hint)
	}
	if known {
		nodes = append(nodes, have.Node)
	}
	fetched, ok := m.fetchFrom(ctx, id, nodes)
	if !ok {
		var out struct {
			Providers []struct {
				ID string `json:"id"`
			} `json:"providers"`
		}
		if err := m.env.Call(ctx, "routing.find_providers", map[string]string{"key": id}, &out); err == nil {
			var homes []string
			for _, p := range out.Providers {
				if !slices.Contains(nodes, p.ID) {
					homes = append(homes, p.ID)
				}
			}
			fetched, ok = m.fetchFrom(ctx, id, homes)
		}
	}
	if !ok {
		if known {
			return have, nil
		}
		return Profile{}, module.Errorf(module.CodeNotFound, "nobody has a profile for %s", id)
	}
	fetched.Fetched = time.Now().UTC()
	if known && fetched.Time.Before(have.Time) {
		fetched = have // theirs is older than our copy: an old node, or a replay
		fetched.Fetched = time.Now().UTC()
	}
	if _, err := m.env.Entities("profile").PutIf(ctx, id, fetched, version); err != nil && module.Code(err) != module.CodeConflict {
		return Profile{}, err
	}
	if !known || !fetched.Time.Equal(have.Time) {
		_ = m.env.Emit(ctx, "updated", map[string]string{"id": id})
	}
	return fetched, nil
}

// fetchFrom asks nodes, in order, for the profile of id until one has it and
// it checks out.
func (m *Module) fetchFrom(ctx context.Context, id string, nodes []string) (Profile, bool) {
	for _, node := range nodes {
		if node == "" || node == m.env.NodeID {
			continue
		}
		ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		var theirs Profile
		err := m.env.CallNode(ctx, node, Name+".fetch", map[string]string{"id": id}, &theirs)
		cancel()
		if err != nil {
			continue
		}
		p, err := verify(theirs.Signed)
		if err != nil || p.ID != id {
			slog.Warn("profile: a node served a bad profile", "node", node, "id", id, "err", err)
			continue
		}
		p.Node = node
		return p, true
	}
	return Profile{}, false
}

// image is an avatar with its bytes, on the wire.
type image struct {
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"`
}

func (m *Module) avatar(ctx context.Context, in struct {
	ID string `json:"id"`
}) (image, error) {
	p, err := m.resolve(ctx, in.ID, "")
	if err != nil {
		return image{}, err
	}
	if p.Avatar == "" {
		return image{}, module.Errorf(module.CodeNotFound, "%s has no avatar", in.ID)
	}
	data, info, err := m.env.Blobs().Get(ctx, p.Avatar)
	if err == nil {
		return image{Key: p.Avatar, ContentType: info.ContentType, Data: data}, nil
	}
	if !notFound(err) || p.Node == m.env.NodeID {
		return image{}, err
	}
	// Not here yet: from their home, kept once it checks out.
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	var theirs image
	if err := m.env.CallNode(ctx, p.Node, Name+".fetch_avatar", map[string]string{"key": p.Avatar}, &theirs); err != nil {
		return image{}, err
	}
	if sum := sha256.Sum256(theirs.Data); hex.EncodeToString(sum[:]) != p.Avatar || len(theirs.Data) > maxAvatar {
		return image{}, module.Errorf(module.CodeUnavailable, "node %.8s served the wrong image", p.Node)
	}
	if err := m.env.Blobs().Put(ctx, p.Avatar, theirs.Data, theirs.ContentType); err != nil {
		return image{}, err
	}
	theirs.Key = p.Avatar
	return theirs, nil
}

// fetch serves a profile whose home this node is, to other nodes.
func (m *Module) fetch(ctx context.Context, in struct {
	ID string `json:"id"`
}) (Profile, error) {
	var p Profile
	if _, err := m.env.Entities("profile").Get(ctx, in.ID, &p); notFound(err) || (err == nil && p.Node != m.env.NodeID) {
		return Profile{}, module.Errorf(module.CodeNotFound, "%s isn't at home here", in.ID)
	} else if err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (m *Module) fetchAvatar(ctx context.Context, in struct {
	Key string `json:"key"`
}) (image, error) {
	data, info, err := m.env.Blobs().Get(ctx, in.Key)
	if err != nil {
		return image{}, err
	}
	return image{Key: in.Key, ContentType: info.ContentType, Data: data}, nil
}
