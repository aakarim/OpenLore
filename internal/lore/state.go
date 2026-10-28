package lore

import (
	"encoding/json"
	"errors"
	"os"
)

// SyncState is the machine-local record of what each identity has pushed.
type SyncState struct {
	Identities map[string]IdentityState `json:"identities"`
}

// IdentityState is one identity's acknowledgement and latest snapshots.
type IdentityState struct {
	Acknowledged bool              `json:"acknowledged"`
	Snapshots    map[string]string `json:"snapshots"`
}

// StateKey names the server and identity a sync is recorded under.
func StateKey(cred Credentials) string { return cred.Server + "#" + cred.Identity }

// LoadState reads .local/sync.json, empty when it does not exist.
func (r Repository) LoadState() (SyncState, error) {
	s := SyncState{Identities: map[string]IdentityState{}}
	b, err := os.ReadFile(r.stateFile())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

// SaveState writes .local/sync.json privately.
func (r Repository) SaveState(s SyncState) error { return privateJSON(r.stateFile(), s) }

// ForgetSource drops the snapshot that identity key recorded for a removed
// source, and the local cache of its store. Other identities' records of a
// same-named source are theirs and stay.
func (r Repository) ForgetSource(key, name, store string) error {
	state, err := r.LoadState()
	if err != nil {
		return err
	}
	if id, ok := state.Identities[key]; ok {
		delete(id.Snapshots, name)
		state.Identities[key] = id
		if err := r.SaveState(state); err != nil {
			return err
		}
	}
	return os.RemoveAll(r.sourceDir(store))
}
