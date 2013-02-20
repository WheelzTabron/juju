package state

import (
	"fmt"
	"labix.org/v2/mgo"
	"labix.org/v2/mgo/txn"
	"sort"
)

const (
	ItemAdded = iota
	ItemModified
	ItemDeleted
)

// ItemChange represents the change of an item in a settings.
type ItemChange struct {
	Type     int
	Key      string
	OldValue interface{}
	NewValue interface{}
}

// String returns the item change in a readable format.
func (ic *ItemChange) String() string {
	switch ic.Type {
	case ItemAdded:
		return fmt.Sprintf("setting added: %v = %v", ic.Key, ic.NewValue)
	case ItemModified:
		return fmt.Sprintf("setting modified: %v = %v (was %v)",
			ic.Key, ic.NewValue, ic.OldValue)
	case ItemDeleted:
		return fmt.Sprintf("setting deleted: %v (was %v)", ic.Key, ic.OldValue)
	}
	return fmt.Sprintf("unknown setting change type %d: %v = %v (was %v)",
		ic.Type, ic.Key, ic.NewValue, ic.OldValue)
}

// itemChangeSlice contains a slice of item changes in a config node.
// It implements the sort interface to sort the items changes by key.
type itemChangeSlice []ItemChange

func (ics itemChangeSlice) Len() int           { return len(ics) }
func (ics itemChangeSlice) Less(i, j int) bool { return ics[i].Key < ics[j].Key }
func (ics itemChangeSlice) Swap(i, j int)      { ics[i], ics[j] = ics[j], ics[i] }

// A Settings manages changes to settings as a delta in memory and merges
// them back in the database when explicitly requested.
type Settings struct {
	st  *State
	key string
	// disk holds the values in the config node before
	// any keys have been changed. It is reset on Read and Write
	// operations.
	disk map[string]interface{}
	// cache holds the current values in the config node.
	// The difference between disk and core
	// determines the delta to be applied when Settings.Write
	// is called.
	core     map[string]interface{}
	txnRevno int64
}

// Keys returns the current keys in alphabetical order.
func (c *Settings) Keys() []string {
	keys := []string{}
	for key := range c.core {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Get returns the value of key and whether it was found.
func (c *Settings) Get(key string) (value interface{}, found bool) {
	value, found = c.core[key]
	return
}

// Map returns all keys and values of the node.
func (c *Settings) Map() map[string]interface{} {
	return copyMap(c.core)
}

// Set sets key to value
func (c *Settings) Set(key string, value interface{}) {
	c.core[key] = value
}

// Update sets multiple key/value pairs.
func (c *Settings) Update(kv map[string]interface{}) {
	for key, value := range kv {
		c.core[key] = value
	}
}

// Delete removes key.
func (c *Settings) Delete(key string) {
	delete(c.core, key)
}

// copyMap copies the keys and values of one map into a new one.
func copyMap(in map[string]interface{}) (out map[string]interface{}) {
	out = make(map[string]interface{})
	for key, value := range in {
		out[key] = value
	}
	return
}

// cacheKeys returns the keys of all caches as a key=>true map.
func cacheKeys(caches ...map[string]interface{}) map[string]bool {
	keys := make(map[string]bool)
	for _, cache := range caches {
		for key := range cache {
			keys[key] = true
		}
	}
	return keys
}

// Write writes changes made to c back onto its node.  Changes are written
// as a delta applied on top of the latest version of the node, to prevent
// overwriting unrelated changes made to the node since it was last read.
func (c *Settings) Write() ([]ItemChange, error) {
	changes := []ItemChange{}
	updates := map[string]interface{}{}
	deletions := map[string]int{}
	for key := range cacheKeys(c.disk, c.core) {
		old, ondisk := c.disk[key]
		new, incore := c.core[key]
		if new == old {
			continue
		}
		var change ItemChange
		switch {
		case incore && ondisk:
			change = ItemChange{ItemModified, key, old, new}
			updates[key] = new
		case incore && !ondisk:
			change = ItemChange{ItemAdded, key, nil, new}
			updates[key] = new
		case ondisk && !incore:
			change = ItemChange{ItemDeleted, key, old, nil}
			deletions[key] = 1
		default:
			panic("unreachable")
		}
		changes = append(changes, change)
	}
	if len(changes) == 0 {
		return []ItemChange{}, nil
	}
	sort.Sort(itemChangeSlice(changes))
	ops := []txn.Op{{
		C:      c.st.settings.Name,
		Id:     c.key,
		Assert: txn.DocExists,
		Update: D{
			{"$set", updates},
			{"$unset", deletions},
		},
	}}
	err := c.st.runner.Run(ops, "", nil)
	if err == txn.ErrAborted {
		return nil, NotFoundf("settings")
	}
	if err != nil {
		return nil, fmt.Errorf("cannot write settings: %v", err)
	}
	c.disk = copyMap(c.core)
	return changes, nil
}

func newSettings(st *State, key string) *Settings {
	return &Settings{
		st:   st,
		key:  key,
		core: make(map[string]interface{}),
	}
}

// cleanMap cleans the map of version and _id fields.
func cleanMap(in map[string]interface{}) {
	delete(in, "_id")
	delete(in, "txn-revno")
	delete(in, "txn-queue")
}

// Read (re)reads the node data into c.
func (c *Settings) Read() error {
	config := map[string]interface{}{}
	err := c.st.settings.FindId(c.key).One(config)
	if err == mgo.ErrNotFound {
		c.disk = nil
		c.core = make(map[string]interface{})
		return NotFoundf("settings")
	}
	if err != nil {
		return fmt.Errorf("cannot read settings: %v", err)
	}
	c.txnRevno = config["txn-revno"].(int64)
	cleanMap(config)
	c.disk = copyMap(config)
	c.core = copyMap(config)
	return nil
}

// readSettings returns the Settings for key.
func readSettings(st *State, key string) (*Settings, error) {
	s := newSettings(st, key)
	if err := s.Read(); err != nil {
		return nil, err
	}
	return s, nil
}

var errSettingsExist = fmt.Errorf("cannot overwrite existing settings")

func createSettingsOp(st *State, key string, values map[string]interface{}) txn.Op {
	return txn.Op{
		C:      st.settings.Name,
		Id:     key,
		Assert: txn.DocMissing,
		Insert: values,
	}
}

// createSettings writes an initial config node.
func createSettings(st *State, key string, values map[string]interface{}) (*Settings, error) {
	s := newSettings(st, key)
	s.core = copyMap(values)
	ops := []txn.Op{createSettingsOp(st, key, values)}
	err := s.st.runner.Run(ops, "", nil)
	if err == txn.ErrAborted {
		return nil, errSettingsExist
	}
	if err != nil {
		return nil, fmt.Errorf("cannot create settings: %v", err)
	}
	return s, nil
}

// removeSettings removes the Settings for key.
func removeSettings(st *State, key string) error {
	err := st.settings.RemoveId(key)
	if err == mgo.ErrNotFound {
		return NotFoundf("settings")
	}
	return nil
}

// replaceSettingsOp returns a txn.Op that deletes the document's contents and
// replaces it with the supplied values, and a function that should be called on
// txn failure to determine whether this operation failed (due to a concurrent
// settings change).
func replaceSettingsOp(st *State, key string, values map[string]interface{}) (txn.Op, func() (bool, error), error) {
	s, err := readSettings(st, key)
	if err != nil {
		return txn.Op{}, nil, err
	}
	deletes := map[string]int{}
	for k := range s.disk {
		if _, found := values[k]; !found {
			deletes[k] = 1
		}
	}
	op := txn.Op{
		C:      st.settings.Name,
		Id:     key,
		Assert: D{{"txn-revno", s.txnRevno}},
		Update: D{
			{"$set", values},
			{"$unset", deletes},
		},
	}
	assertFailed := func() (bool, error) {
		latest, err := readSettings(st, key)
		if err != nil {
			return false, err
		}
		return latest.txnRevno != s.txnRevno, nil
	}
	return op, assertFailed, nil
}
