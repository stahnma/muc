package storage

import "server/models"

type Storage interface {
	SaveSystem(hostname string, system models.System) error
	GetSystem(hostname string) (models.System, error)
	GetAllSystems() ([]models.System, error)
	DeleteSystem(hostname string) error
	SubscribeToUpdates() <-chan models.System // This should be declared

	// Groups are server-owned: a host neither declares nor sees them. They are
	// kept apart from System because a client overwrites its whole System
	// record on every check-in.
	GetAllGroups() ([]models.Group, error)
	GetGroup(name string) (models.Group, error)
	SaveGroup(group models.Group) error
	DeleteGroup(name string) error
	RenameGroup(oldName, newName string) error
	SetHostGroups(hostname string, groups []string) error

	// The operations log records what hosts were asked to do and what they
	// said back. Unlike the rest of storage it is only ever appended to.
	AppendOperation(op models.Operation) error
	GetOperations(hostname string, before uint64, limit int) ([]models.Operation, error)
}
