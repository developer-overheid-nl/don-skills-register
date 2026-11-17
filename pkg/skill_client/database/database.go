package database

import (
	commondatabase "github.com/developer-overheid-nl/don-register-common/database"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/models"
	"gorm.io/gorm"
)

// Models zijn de tabellen die bij het opstarten gemigreerd worden.
var Models = []any{
	&models.Organisation{},
	&models.Source{},
	&models.Plugin{},
	&models.Skill{},
}

// Connect maakt verbinding met Postgres en voert de migraties uit.
func Connect(connStr string) (*gorm.DB, error) {
	return commondatabase.ConnectPostgres(connStr, Models...)
}
