package storage

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"server/metrics"
	"server/models"
	"slices"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// groupsBucket holds one record per group: key is the group's name folded to
// lower case, value is the JSON models.Group carrying the name as it was typed.
//
// Keying on the folded name is what makes "prod" and "Prod" the same group
// rather than two that look alike — the uniqueness check is the key itself
// rather than a scan that someone later forgets to do — and it lets a URL name
// a group in whatever case the operator typed.
const groupsBucket = "groups"

// groupKey is the bucket key for a group name.
func groupKey(name string) []byte {
	return []byte(strings.ToLower(strings.TrimSpace(name)))
}

// normalizeMembers sorts and de-duplicates a member list so a stored group
// reads the same way every time and the dashboard never has to sort it.
func normalizeMembers(members []string) []string {
	seen := make(map[string]bool, len(members))
	out := make([]string, 0, len(members))
	for _, m := range members {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// readGroup pulls one group out of an open transaction. ok is false when the
// bucket or the key is absent, which are both "no such group" rather than
// errors — the bucket does not exist until the first group is created.
func readGroup(tx *bolt.Tx, name string) (models.Group, bool, error) {
	bucket := tx.Bucket([]byte(groupsBucket))
	if bucket == nil {
		return models.Group{}, false, nil
	}

	data := bucket.Get(groupKey(name))
	if data == nil {
		return models.Group{}, false, nil
	}

	var group models.Group
	if err := json.Unmarshal(data, &group); err != nil {
		return models.Group{}, false, fmt.Errorf("failed to unmarshal group %q: %w", name, err)
	}

	return group, true, nil
}

// writeGroup stores a group, creating the bucket if this is the first one.
func writeGroup(tx *bolt.Tx, group models.Group) error {
	bucket, err := tx.CreateBucketIfNotExists([]byte(groupsBucket))
	if err != nil {
		return err
	}

	group.Members = normalizeMembers(group.Members)
	data, err := json.Marshal(group)
	if err != nil {
		return err
	}

	return bucket.Put(groupKey(group.Name), data)
}

// GetAllGroups returns every group, ordered by name. A database with no groups
// yet returns an empty slice rather than an error.
func (s *BboltStorage) GetAllGroups() ([]models.Group, error) {
	start := time.Now()
	defer func() {
		metrics.StorageOperationDuration.WithLabelValues("get_all_groups").Observe(time.Since(start).Seconds())
	}()

	groups := []models.Group{}

	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(groupsBucket))
		if bucket == nil {
			return nil
		}

		return bucket.ForEach(func(k, v []byte) error {
			var group models.Group
			if err := json.Unmarshal(v, &group); err != nil {
				// Same tolerance GetAllSystems shows: one unreadable record
				// should not hide every other group.
				slog.Error("Failed to unmarshal group, skipping", "key", string(k), "error", err)
				return nil
			}
			groups = append(groups, group)
			return nil
		})
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("get_all_groups").Inc()
		return nil, err
	}

	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Name) < strings.ToLower(groups[j].Name)
	})

	return groups, nil
}

// GetGroup returns one group by name, matched without regard to case.
func (s *BboltStorage) GetGroup(name string) (models.Group, error) {
	start := time.Now()
	defer func() {
		metrics.StorageOperationDuration.WithLabelValues("get_group").Observe(time.Since(start).Seconds())
	}()

	var group models.Group

	err := s.db.View(func(tx *bolt.Tx) error {
		found, ok, err := readGroup(tx, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("group '%s' not found", name)
		}
		group = found
		return nil
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("get_group").Inc()
		return models.Group{}, err
	}

	return group, nil
}

// SaveGroup creates a group or replaces it wholesale, members and all. A group
// that already exists under a different spelling of the same name keeps the
// spelling passed here.
func (s *BboltStorage) SaveGroup(group models.Group) error {
	start := time.Now()
	defer func() {
		metrics.StorageOperationDuration.WithLabelValues("save_group").Observe(time.Since(start).Seconds())
	}()

	s.Lock()
	defer s.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		return writeGroup(tx, group)
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("save_group").Inc()
		slog.Error("Failed to save group", "group", group.Name, "error", err)
		return err
	}

	return nil
}

// DeleteGroup removes a group. The hosts in it are untouched — a group is a
// label, and dropping the label is not dropping the machines.
func (s *BboltStorage) DeleteGroup(name string) error {
	start := time.Now()
	defer func() {
		metrics.StorageOperationDuration.WithLabelValues("delete_group").Observe(time.Since(start).Seconds())
	}()

	s.Lock()
	defer s.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		if _, ok, err := readGroup(tx, name); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("group '%s' not found", name)
		}
		return tx.Bucket([]byte(groupsBucket)).Delete(groupKey(name))
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("delete_group").Inc()
		slog.Error("Failed to delete group", "group", name, "error", err)
		return err
	}

	slog.Info("Group deleted", "group", name)
	return nil
}

// RenameGroup renames a group, keeping its members.
//
// It is one transaction rather than a delete and a create because the halfway
// state — the old group gone, the new one not yet written — would lose every
// member if the process died between them.
func (s *BboltStorage) RenameGroup(oldName, newName string) error {
	start := time.Now()
	defer func() {
		metrics.StorageOperationDuration.WithLabelValues("rename_group").Observe(time.Since(start).Seconds())
	}()

	s.Lock()
	defer s.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		group, ok, err := readGroup(tx, oldName)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("group '%s' not found", oldName)
		}

		// A rename that only changes case keeps the same key, so it is a
		// relabel in place rather than a collision with itself.
		if !models.SameGroupName(oldName, newName) {
			if _, taken, err := readGroup(tx, newName); err != nil {
				return err
			} else if taken {
				return fmt.Errorf("group '%s' already exists", newName)
			}
		}

		group.Name = newName
		if err := writeGroup(tx, group); err != nil {
			return err
		}

		if !models.SameGroupName(oldName, newName) {
			return tx.Bucket([]byte(groupsBucket)).Delete(groupKey(oldName))
		}
		return nil
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("rename_group").Inc()
		slog.Error("Failed to rename group", "group", oldName, "to", newName, "error", err)
		return err
	}

	slog.Info("Group renamed", "group", oldName, "to", newName)
	return nil
}

// SetHostGroups replaces one host's memberships: it joins every group named
// here and leaves every other one.
//
// This is a single transaction because it is a single edit as far as the
// operator is concerned — they ticked boxes on one host and pressed save. Doing
// it as one write per group would leave the host in a mixture of its old and
// new groups if any of them failed.
//
// Every named group must already exist; creating one is a separate, deliberate
// act, so that a typo in a group name joins nothing rather than quietly
// founding a group of one.
func (s *BboltStorage) SetHostGroups(hostname string, groups []string) error {
	start := time.Now()
	defer func() {
		metrics.StorageOperationDuration.WithLabelValues("set_host_groups").Observe(time.Since(start).Seconds())
	}()

	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return fmt.Errorf("a hostname is required")
	}

	s.Lock()
	defer s.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		wanted := make(map[string]bool, len(groups))
		for _, name := range groups {
			group, ok, err := readGroup(tx, name)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("group '%s' not found", name)
			}
			wanted[string(groupKey(group.Name))] = true
		}

		bucket := tx.Bucket([]byte(groupsBucket))
		if bucket == nil {
			// No groups exist, so there is nothing to join and nothing to
			// leave. Asking for none of them is the only coherent request.
			if len(wanted) > 0 {
				return fmt.Errorf("no groups exist")
			}
			return nil
		}

		// Walk every group once, adding or removing this host as the wanted
		// set says, so a membership the caller did not mention is dropped.
		var changed []models.Group
		err := bucket.ForEach(func(k, v []byte) error {
			var group models.Group
			if err := json.Unmarshal(v, &group); err != nil {
				slog.Error("Failed to unmarshal group, skipping", "key", string(k), "error", err)
				return nil
			}

			member := slices.Contains(group.Members, hostname)
			switch {
			case wanted[string(k)] && !member:
				group.Members = append(group.Members, hostname)
			case !wanted[string(k)] && member:
				group.Members = slices.DeleteFunc(group.Members, func(m string) bool { return m == hostname })
			default:
				return nil
			}

			changed = append(changed, group)
			return nil
		})
		if err != nil {
			return err
		}

		for _, group := range changed {
			if err := writeGroup(tx, group); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("set_host_groups").Inc()
		slog.Error("Failed to set host groups", "hostname", hostname, "error", err)
		return err
	}

	return nil
}

// removeHostFromGroups drops a hostname from every group inside an open
// transaction. DeleteSystem calls it so that deleting a host from the dashboard
// does not leave its name behind in groups it used to be in.
func removeHostFromGroups(tx *bolt.Tx, hostname string) error {
	bucket := tx.Bucket([]byte(groupsBucket))
	if bucket == nil {
		return nil
	}

	var changed []models.Group
	err := bucket.ForEach(func(k, v []byte) error {
		var group models.Group
		if err := json.Unmarshal(v, &group); err != nil {
			slog.Error("Failed to unmarshal group, skipping", "key", string(k), "error", err)
			return nil
		}
		if !slices.Contains(group.Members, hostname) {
			return nil
		}
		group.Members = slices.DeleteFunc(group.Members, func(m string) bool { return m == hostname })
		changed = append(changed, group)
		return nil
	})
	if err != nil {
		return err
	}

	for _, group := range changed {
		if err := writeGroup(tx, group); err != nil {
			return err
		}
	}
	return nil
}
