package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/joho/godotenv"

	api "github.com/developer-overheid-nl/don-skills-register/pkg/skill_client"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/database"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/handler"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/harvester"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/helpers/util"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/jobs"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/repositories"
	"github.com/developer-overheid-nl/don-skills-register/pkg/skill_client/services"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("Error loading .env file ", err)
	}

	version, err := util.LoadOASVersion("./api/openapi.json")
	if err != nil {
		log.Fatalf("failed to load OAS version: %v", err)
	}

	db, err := database.Connect(postgresDSN())
	if err != nil {
		log.Fatalf("Geen databaseverbinding: %v", err)
	}

	repo := repositories.NewSkillsRepository(db)
	service := services.NewSkillService(repo, harvester.New(harvester.NewGitHubClientFromEnv()))
	controller := handler.NewSkillController(service)

	if os.Getenv("DISABLE_HARVEST") != "true" {
		jobs.NewHarvestJob(context.Background(), service)
	}

	router := api.NewRouter(version, controller)
	log.Println("Server is running on port 1337")
	log.Fatal(http.ListenAndServe(":1337", router))
}

func postgresDSN() string {
	u := &url.URL{
		Scheme: "postgres",
		Host:   os.Getenv("DB_HOSTNAME") + ":5432",
		Path:   os.Getenv("DB_DBNAME"),
		User:   url.UserPassword(os.Getenv("DB_USERNAME"), os.Getenv("DB_PASSWORD")),
	}
	q := u.Query()
	q.Set("search_path", os.Getenv("DB_SCHEMA"))
	u.RawQuery = q.Encode()
	return u.String()
}
