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
}
