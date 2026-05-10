package storage

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"server/metrics"
	"server/models"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

type BboltStorage struct {
	sync.Mutex
	db        *bolt.DB
	updates   chan models.System
	closed    bool
	closedMux sync.RWMutex
	closeOnce sync.Once
}

func NewBboltStorage(dbPath string) (*BboltStorage, error) {
	db, err := bolt.Open(dbPath, 0666, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		if err.Error() == "timeout" {
			return nil, fmt.Errorf("database %q is locked — is another instance already running?", dbPath)
		}
		return nil, err
	}

	return &BboltStorage{
		db:      db,
		updates: make(chan models.System, 100), // Buffered channel for updates
	}, nil
}

func (s *BboltStorage) SaveSystem(hostname string, system models.System) error {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metrics.StorageOperationDuration.WithLabelValues("save_system").Observe(duration)
	}()

	s.Lock()
	defer s.Unlock()
	system.LastSeen = time.Now().UTC().Format(time.RFC3339) // Ensure UTC timestamp

	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte("systems"))
		if err != nil {
			return err
		}
		data, err := json.Marshal(system)
		if err != nil {
			return err
		}
		return bucket.Put([]byte(hostname), data)
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("save_system").Inc()
		slog.Error("Failed to save system", "error", err)
		return err
	}

	// Send update to the updates channel (only if not closed)
	// Use recover to handle race condition where channel closes between check and send
	s.closedMux.RLock()
	isClosed := s.closed
	s.closedMux.RUnlock()

	if !isClosed {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// Channel was closed between check and send - ignore silently
					// This is expected during shutdown
					slog.Debug("Dropped update due to channel closure", "hostname", hostname)
				}
			}()
			select {
			case s.updates <- system:
				slog.Debug("Successfully sent system update to channel", "hostname", hostname)
			default:
				slog.Warn("Updates channel is full; dropping update", "hostname", hostname)
			}
		}()
	} else {
		slog.Debug("Updates channel is closed, not sending update", "hostname", hostname)
	}

	return nil
}

func (s *BboltStorage) GetSystem(hostname string) (models.System, error) {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metrics.StorageOperationDuration.WithLabelValues("get_system").Observe(duration)
	}()

	var system models.System

	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("systems"))
		if bucket == nil {
			return fmt.Errorf("bucket 'systems' not found")
		}

		data := bucket.Get([]byte(hostname))
		if data == nil {
			return fmt.Errorf("system with hostname '%s' not found", hostname)
		}

		if err := json.Unmarshal(data, &system); err != nil {
			return fmt.Errorf("failed to unmarshal system data: %v", err)
		}

		return nil
	})

	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("get_system").Inc()
		return models.System{}, err
	}

	return system, nil
}

func (s *BboltStorage) GetAllSystems() ([]models.System, error) {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metrics.StorageOperationDuration.WithLabelValues("get_all_systems").Observe(duration)
	}()

	var systems []models.System

	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("systems"))
		// If bucket doesn't exist, return empty array (normal state when no systems have checked in yet)
		if bucket == nil {
			return nil
		}

		return bucket.ForEach(func(k, v []byte) error {
			var system models.System
			if err := json.Unmarshal(v, &system); err != nil {
				slog.Error("Failed to unmarshal system data, skipping", "key", string(k), "error", err)
				// Continue iteration by returning nil instead of error
				// This allows other valid records to be loaded even if one is corrupted
				return nil
			}
			systems = append(systems, system)
			return nil
		})
	})

	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("get_all_systems").Inc()
		return nil, err
	}

	return systems, nil
}

func (s *BboltStorage) DeleteSystem(hostname string) error {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metrics.StorageOperationDuration.WithLabelValues("delete_system").Observe(duration)
	}()

	s.Lock()
	defer s.Unlock()

	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("systems"))
		if bucket == nil {
			return fmt.Errorf("bucket 'systems' not found")
		}

		// Check if system exists before deleting
		data := bucket.Get([]byte(hostname))
		if data == nil {
			return fmt.Errorf("system with hostname '%s' not found", hostname)
		}

		return bucket.Delete([]byte(hostname))
	})

	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("delete_system").Inc()
		slog.Error("Failed to delete system", "hostname", hostname, "error", err)
		return err
	}

	slog.Info("System deleted", "hostname", hostname)
	return nil
}

func (s *BboltStorage) SubscribeToUpdates() <-chan models.System {
	return s.updates
}

func (s *BboltStorage) Close() error {
	s.closeOnce.Do(func() {
		s.closedMux.Lock()
		s.closed = true
		close(s.updates)
		s.closedMux.Unlock()
	})
	return s.db.Close()
}
