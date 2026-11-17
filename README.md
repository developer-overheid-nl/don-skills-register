# Skills-register API

API van het Skills register (skills.developer.overheid.nl)

## Overview

- API version: 0.1.0
- Runtime: Go
- Database: PostgreSQL

Deze API registreert en ontsluit AI skills: `SKILL.md` bestanden volgens de
[Agent Skills](https://agentskills.io)-conventie, zoals Claude Code, Codex en
Cursor die gebruiken. Skills worden niet los aangeleverd, maar geharvest uit
**bronnen**.

## Bronnen, plugins en skills

- **Bron**: een GitHub-repository die het register harvest. Bronnen staan in
  [`sources.json`](sources.json) en kunnen niet via de API worden toegevoegd.
- **Plugin**: een bundel skills uit een marketplace, met naam, versie, auteur
  en tags uit `marketplace.json`.
- **Skill**: één `SKILL.md` bestand, met de frontmatter en de volledige inhoud.

Er zijn twee soorten bronnen:

| Type          | Wat                                                                        | Voorbeeld                                                                                               |
| ------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `marketplace` | Repository met `.claude-plugin/marketplace.json` die naar plugins verwijst | [developer-overheid-nl/skills-marketplace](https://github.com/developer-overheid-nl/skills-marketplace) |
| `repository`  | Losse repository met één of meer `SKILL.md` bestanden                      | een repo met `skills/<naam>/SKILL.md`                                                                   |

Bij een marketplace volgt de harvester per plugin het `source` veld:

- een relatief pad (`"./plugins/x"`) binnen de marketplace-repository;
- `{"source": "github", "repo": "owner/repo"}`;
- `{"source": "url"}` en `{"source": "git-subdir"}` naar github.com.

Andere hosts en npm-plugins worden nog overgeslagen en als waarschuwing op de
bron (`lastError`) gezet. Binnen een plugin of repository telt elk `SKILL.md`
bestand als skill (buiten `node_modules`). De frontmatter moet minimaal `name` en
`description` bevatten; bestanden zonder die velden worden overgeslagen met een
waarschuwing.

Ids zijn stabiel: dezelfde skill op hetzelfde pad krijgt bij elke harvest
hetzelfde id. Skills die uit een bron verdwijnen, worden bij de volgende
harvest verwijderd.

## Bron toevoegen

Een bron toevoegen, wijzigen of verwijderen gaat via een pull request op
[`sources.json`](sources.json):

```json
[
  {
    "type": "marketplace",
    "url": "https://github.com/developer-overheid-nl/skills-marketplace"
  },
  {
    "type": "repository",
    "url": "https://github.com/organisatie/skills",
    "organisationUri": "https://identifier.overheid.nl/tooi/id/oorg/oorg10103"
  }
]
```

| Veld              | Verplicht | Omschrijving                                                         |
| ----------------- | --------- | -------------------------------------------------------------------- |
| `type`            | ja        | `marketplace` of `repository`                                        |
| `url`             | ja        | GitHub-repository; `.git` en `/tree/...` worden weggehaald           |
| `organisationUri` | nee       | TOOI-URI van de beherende organisatie; het label komt uit TOOI       |

Bij het opstarten maakt de API de bronnen in de database gelijk aan dit
bestand: nieuwe bronnen worden toegevoegd, type en organisatie van bestaande
bronnen bijgewerkt, en bronnen die er niet meer in staan worden met hun
plugins en skills verwijderd. Een ongeldig of ontbrekend bestand stopt het
opstarten, zodat er nooit per ongeluk bronnen worden gewist.

## Harvesten

Harvesten gebeurt alleen door een job, net als bij de andere registers. Er is
geen endpoint om een harvest te starten. De job draait direct na het opstarten
en daarna iedere ochtend om **06:00** (maximaal een uur). Per bron:

1. de commit bepalen: de standaardbranch, of de `ref`/`sha` die een plugin in
   `marketplace.json` opgeeft;
2. de bestandenlijst ophalen en `marketplace.json` en de `SKILL.md` bestanden
   lezen;
3. alle plugins en skills van de bron in één transactie vervangen door het
   resultaat;
4. de skills naar Typesense sturen.

Mislukt een bron, dan blijft de vorige inhoud staan en komt de fout in
`lastError` van de bron (`GET /v1/sources`).

De harvester leest repositories via de GitHub API (commits en trees) en de
bestanden via `raw.githubusercontent.com`. Zonder token geldt de GitHub-limiet
van 60 API-calls per uur; met `GITHUB_TOKEN` is dat 5000. Een harvest kost
ongeveer twee API-calls per repository.

Omgevingsvariabelen:

- `GITHUB_TOKEN`: token voor de GitHub API, nodig in test en productie.
- `GITHUB_API_BASE`: standaard `https://api.github.com`.
- `GITHUB_RAW_BASE`: standaard `https://raw.githubusercontent.com`.
- `SOURCES_FILE`: pad naar de bronnen, standaard `./sources.json`.
- `DISABLE_HARVEST`: zet op `true` om de harvest-job niet te starten.

## Endpoints

| Methode | Pad                        | Omschrijving                                                |
| ------- | -------------------------- | ----------------------------------------------------------- |
| GET     | `/v1/skills`               | Skills, met filters `q`, `plugin`, `source`, `organisation` |
| GET     | `/v1/skills/filters`       | Filteropties met counts                                     |
| GET     | `/v1/skills/{id}`          | Skill met frontmatter en inhoud                             |
| GET     | `/v1/skills/{id}/skill.md` | Ruwe `SKILL.md`                                             |
| GET     | `/v1/plugins`              | Plugins uit marketplaces                                    |
| GET     | `/v1/plugins/{id}`         | Eén plugin                                                  |
| GET     | `/v1/sources`              | Bronnen met status van de laatste harvest                   |
| GET     | `/v1/sources/{id}`         | Eén bron                                                    |
| GET     | `/v1/organisations`        | Organisaties                                                |
| POST    | `/v1/organisations`        | Organisatie aanmaken (label via TOOI)                       |
| GET     | `/v1/openapi.json`         | De OpenAPI-specificatie                                     |

Lijsten zijn gepagineerd met `page` en `perPage` (maximaal 100) en geven de
headers `Link`, `Total-Count`, `Current-Page`, `Per-Page` en `Total-Pages`
terug. Fouten zijn `application/problem+json`.

Zie [`api/openapi.json`](api/openapi.json) voor de volledige specificatie
(OpenAPI 3.1). Twee tests houden de spec en de code gelijk:

- `pkg/skill_client/openapi_test.go`: elke route staat in de spec en andersom;
- `pkg/skill_client/openapi_response_test.go`: echte responses (statuscodes,
  headers en bodies) worden gevalideerd tegen de schema's in de spec.

De spec is daarnaast gelint met Spectral (`spectral:oas` en de
[ADR-ruleset](https://github.com/logius-standaarden/API-Design-Rules) van
Logius) en Redocly.

## Lokaal draaien

1. Start de afhankelijkheden:

   ```bash
   docker compose up -d
   ```

2. Maak een `.env` bestand aan met minimaal:

   ```env
   DB_HOSTNAME=localhost
   DB_USERNAME=don
   DB_PASSWORD=don
   DB_DBNAME=don_skills_v1
   DB_SCHEMA=public
   GITHUB_TOKEN=<persoonlijk token, alleen leesrechten op publieke repos>
   ENABLE_TYPESENSE=false
   ```

3. Start de server:

   ```bash
   go run cmd/main.go
   ```

   De API luistert standaard op poort **1337**. Na het opstarten wordt direct
   geharvest; volg de voortgang in de log (`[harvest] ...`).

4. Tests en lint:

   ```bash
   go test ./...
   golangci-lint run ./...
   ```

## Typesense integratie

Na elke harvest worden de skills naar Typesense gestuurd, zodat ze vindbaar
zijn in de zoekfunctie. Stel hiervoor de volgende omgevingsvariabelen in:

- `TYPESENSE_ENDPOINT`: basis-URL van de Typesense cluster.
- `TYPESENSE_API_KEY`: API key met schrijfrechten.
- `TYPESENSE_COLLECTION`: naam van de collectie, standaard `skills-register`.
- `TYPESENSE_DETAIL_BASE_URL`: basis-URL voor detailpagina's in de frontend,
  standaard `https://skills.developer.overheid.nl/skills`.
- `TYPESENSE_LANGUAGE`: taalcode voor indexdocumenten, standaard `nl`.
- `TYPESENSE_ITEM_PRIORITY`: prioriteit in de zoekindex, standaard `1`.
- `TYPESENSE_DEFAULT_TAGS`: komma-gescheiden tags, standaard
  `skills-register,skill`.
- `ENABLE_TYPESENSE`: zet op `false` om Typesense indexing volledig uit te
  schakelen, standaard `true`.

## Database

De applicatie gebruikt PostgreSQL. De docker-compose start lokaal een Postgres
container met:

- Host: `localhost`
- Port: `5432`
- Username: `don`
- Password: `don`
- Database: `don_skills_v1`

De applicatie voert bij het opstarten de Gorm migraties uit voor organisaties,
bronnen, plugins en skills.

## Changelog (Changie)

Voor user-facing wijzigingen (fix/feature/breaking) verwachten we per PR een
Changie-fragment in `.changes/unreleased`.

Eenmalig installeren:

```bash
go install github.com/miniscruff/changie@latest
```

Fragment aanmaken:

```bash
changie new
```

Normaal is een fragment niet nodig voor interne refactors zonder zichtbaar
effect, docs-only wijzigingen en CI-only tweaks.

Bij elke merge naar `main` draait via GitHub Actions `changie batch auto` en
daarna `changie merge`, waarna automatisch een PR met de changelog-updates wordt
aangemaakt.

## CI

De workflows in [`.github/workflows`](.github/workflows) roepen de gedeelde
workflows uit [developer-overheid-nl/.github](https://github.com/developer-overheid-nl/.github)
aan (`@v1`), net als bij de andere registers:

| Workflow                   | Wanneer                              | Wat                                                        |
| -------------------------- | ------------------------------------ | ---------------------------------------------------------- |
| `go-ci.yml`                | pull requests en push naar `main`    | `golangci-lint`, tests met coverage en Docker-build         |
| `json-ci.yml`              | pull requests                        | JSON-lint en OpenAPI-validatie (Super-Linter)              |
| `oas-adr.yml`              | pull requests op `api/openapi.json`  | OpenAPI tegen de ADR 2.2.0 met de DON Checker              |
| `changie-auto-release.yml` | pull requests en push naar `main`    | herinnering bij ontbrekend fragment; op `main` changelog-PR |
| `deploy-test.yml`          | push naar andere branches            | image bouwen en naar test deployen bij `[deploy-test]`     |
| `deploy-prod.yml`          | push naar `main`                     | image bouwen en een release-PR in de infra-repo aanmaken   |

Dependabot houdt de Go-modules en de actions bij.

## Deployen

De deployment van deze API verloopt via GitHub Actions en een aparte infra
repository, met de gedeelde workflows uit
[developer-overheid-nl/.github](https://github.com/developer-overheid-nl/.github).

### Benodigde variabelen en secrets

- Organization variable `INFRA_REPO`, bijvoorbeeld
  `developer-overheid-nl/don-infra`.
- Repository variable `KUSTOMIZE_PATH`, met als basispad bijvoorbeeld
  `apps/skills-register/overlays/`.
- Secrets `RELEASE_PROCES_APP_ID` en `RELEASE_PROCES_APP_PRIVATE_KEY` voor het
  aanpassen van de infra repository.

### Deploy naar test

- De workflow draait op pushes naar branches behalve `main`.
- Alleen commits met `[deploy-test]` in de commit message worden echt gedeployed.
- Er wordt een image gebouwd en gepusht naar `ghcr.io/<owner>/<repo>` met tags
  `test` en de commit SHA.
- Daarna wordt in `INFRA_REPO` het bestand
  `${KUSTOMIZE_PATH}test/kustomization.yaml` bijgewerkt naar de nieuwe image tag.

### Deploy naar productie

- De workflow draait bij een push naar `main`.
- Er wordt een image gebouwd en gepusht met tags `latest` en de commit SHA.
- In `INFRA_REPO` wordt een release branch en pull request aangemaakt of
  bijgewerkt; de productie-uitrol gebeurt door die pull request te mergen.

## Nog open

- **Losse skills.** Het type `repository` maakt losse skill-repositories al
  mogelijk. Nog besluiten of we die willen opnemen, of alleen marketplaces.
- **Organisatie per bron of per plugin.** De organisatie hangt nu aan de bron.
  In één marketplace zitten plugins van verschillende organisaties (MinBZK,
  Rijks ICT Gilde, Geonovum), dus waarschijnlijk moet de organisatie per plugin,
  bijvoorbeeld afgeleid uit de GitHub-owner of uit `author` in
  `marketplace.json`. De seed-marketplace heeft nu geen organisatie.
- **Alleen GitHub.** npm-plugins en andere git-hosts (GitLab, code.overheid.nl)
  worden overgeslagen met een waarschuwing.
- **Domein en gateway.** `skills.developer.overheid.nl` en het gateway-pad
  `/skills-register/v1` zijn aannames.
- **Infra.** Nog nodig: een overlay in `don-infra`, de repository variable
  `KUSTOMIZE_PATH`, een `GITHUB_TOKEN` secret in het cluster, en de scopes
  `skills:read`, `sources:read`, `organisations:read` en `organisations:write`
  in Keycloak.
- **Frontend.** Er is nog geen frontend; de Typesense-detail-URL wijst naar
  `https://skills.developer.overheid.nl/skills/{id}`.

## Licentie

Copyright (c) Geonovum. Licensed under the EUPL-1.2.

Zie ook [publiccode.yml](publiccode.yml) voor projectmetadata.
