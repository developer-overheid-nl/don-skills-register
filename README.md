# Skills-register API

API van het Skills register (skills.developer.overheid.nl)

## Overview

- API version: 0.1.0
- Runtime: Go
- Database: PostgreSQL

Deze API registreert en ontsluit AI skills: `SKILL.md` bestanden volgens de
[Agent Skills](https://agentskills.io)-conventie, zoals Claude Code, Codex en
Cursor die gebruiken. Skills worden geharvest uit **bronnen**: marketplaces,
plugins of losse skills op GitHub, elk gekoppeld aan een organisatie.

## Bronnen, plugins en skills

- **Bron**: een marketplace, plugin of losse skill op GitHub, gekoppeld aan een
  organisatie. Bronnen worden geregistreerd met `POST /v1/sources`.
- **Plugin**: een bundel skills, met naam, versie, auteur en tags uit
  `marketplace.json` of `plugin.json`.
- **Skill**: één `SKILL.md` bestand, met de frontmatter en de volledige inhoud.

Er zijn drie soorten bronnen:

| Type          | Wat de harvester doet                                                                                           | Voorbeeld-URL                                                                  |
| ------------- | --------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| `marketplace` | Leest `.claude-plugin/marketplace.json`, volgt elke plugin en haalt daar de skills uit                          | `https://github.com/developer-overheid-nl/skills-marketplace`                  |
| `plugin`      | Leest `.claude-plugin/plugin.json` (optioneel) en haalt alle `SKILL.md` bestanden onder de plugin-map op        | `https://github.com/developer-overheid-nl/skills-internet`                     |
| `skill`       | Haalt één `SKILL.md` op (een `…/blob/…` URL), of alle `SKILL.md` bestanden onder een map, zonder plugin         | `https://github.com/org/repo/blob/main/skills/x/SKILL.md`                      |

De URL is een GitHub-repository, of een map of bestand daarin
(`…/tree/<ref>/<pad>` of `…/blob/<ref>/<pad>`). Zonder ref gebruikt de harvester
de standaardbranch. Een ref met een `/` erin (zoals `feat/x`) wordt niet
ondersteund; gebruik dan een tag of commit-SHA. Een `plugin` zonder `plugin.json`
krijgt de naam van de map of repository, zoals in Claude Code.

Bij een marketplace volgt de harvester per plugin het `source` veld:

- een relatief pad (`"./plugins/x"`) vanaf de marketplace-root (de map met
  `.claude-plugin/`);
- `{"source": "github", "repo": "owner/repo"}`;
- `{"source": "url"}` en `{"source": "git-subdir"}` naar github.com.

Andere hosts en npm-plugins worden nog overgeslagen en als waarschuwing op de
bron (`lastError`) gezet. Binnen een plugin telt elk `SKILL.md` bestand als
skill (buiten `node_modules`). De frontmatter moet minimaal `name` en
`description` bevatten; bestanden zonder die velden worden overgeslagen met een
waarschuwing. Levert een `plugin` of `skill` bron helemaal geen geldige skill
op, dan mislukt de harvest en staat de reden in `lastError`.

Ids zijn stabiel: dezelfde skill op hetzelfde pad krijgt bij elke harvest
hetzelfde id. Skills die uit een bron verdwijnen, worden bij de volgende
harvest verwijderd.

### Eén keer in het register

Het id van een plugin of skill hangt af van de repository en het pad, niet van
de bron. Verwijzen twee bronnen naar dezelfde skill, bijvoorbeeld een
marketplace en dezelfde plugin die los is geregistreerd, dan staat die skill er
één keer in. Hij hoort bij de meest specifieke bron: `skill` gaat voor `plugin`,
en `plugin` gaat voor `marketplace`. Bij hetzelfde type gaat de oudste bron
voor. De skill krijgt de organisatie van die bron.

Zo geef je een plugin uit een marketplace een eigen organisatie: registreer hem
los als `plugin`. Na de volgende harvest van de marketplace staan de skills alleen
nog onder de losse plugin; `createdAt` blijft behouden. Valt de losse bron weg,
dan neemt de marketplace de skills bij zijn volgende harvest weer op.

### Plugin en marketplaces bij een skill

Tijdens het harvesten onthoudt het register per bron welke skills erin
voorkomen en in welke plugin (tabel `skill_occurrences`). Dat gebeurt vóór het
ontdubbelen, dus ook voor skills die bij een andere bron horen. Een skill laat
daardoor altijd zien waar hij vandaan komt:

```json
"plugin": { "id": "…", "name": "internet", "version": "1.2.0" },
"marketplaces": [
  { "id": "…", "name": "overheid-plugins", "url": "https://github.com/developer-overheid-nl/skills-marketplace" }
],
"source": { "id": "…", "type": "plugin", "name": "internet", "url": "https://github.com/developer-overheid-nl/skills-internet" }
```

- `plugin`: de plugin van de skill. Is de skill los geregistreerd, dan de
  plugin waarin hij via een andere bron voorkomt.
- `marketplaces`: alle marketplaces waarin de skill voorkomt, op naam.
- `source`: de bron waar de skill onder valt (zie hierboven).

Verdwijnt een skill uit een bron, dan verdwijnt die bron bij de volgende harvest
ook bij de skill.

## Bron toevoegen

Net als repositories en git-organisaties in het OSS-register, met een
client-credentials token met scope `sources:write`:

```bash
curl -X POST https://api.developer.overheid.nl/skills-register/v1/sources \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "marketplace",
    "url": "https://github.com/developer-overheid-nl/skills-marketplace",
    "organisationUri": "https://identifier.overheid.nl/tooi/id/oorg/oorg10103"
  }'
```

| Veld              | Verplicht | Omschrijving                                                                     |
| ----------------- | --------- | -------------------------------------------------------------------------------- |
| `type`            | ja        | `marketplace`, `plugin` of `skill`                                               |
| `url`             | ja        | GitHub-URL van de repository, map of `SKILL.md`; `.git` en een `/` aan het eind worden weggehaald |
| `organisationUri` | ja        | TOOI-URI van de beherende organisatie; onbekende organisaties worden met het label uit TOOI aangemaakt |

Antwoorden:

- `201`: de bron is geregistreerd. Hij wordt direct op de achtergrond
  geharvest; het resultaat staat daarna in `lastHarvestedAt` en `lastError`
  van de bron.
- `400`: ongeldig type, geen GitHub-URL, of een organisatie die niet in TOOI
  staat.
- `409`: de bron (dezelfde genormaliseerde URL) bestaat al.

Wijzigen en verwijderen kan nog niet via de API (zie [Nog open](#nog-open)).

## Harvesten

Harvesten gebeurt door een job, net als bij de andere registers. Er is geen
endpoint om een harvest te starten; alleen een nieuw geregistreerde bron wordt
één keer direct geharvest. De job draait direct na het opstarten en daarna
iedere ochtend om **06:00** (maximaal een uur). Per bron:

1. de commit bepalen: de ref uit de URL of de standaardbranch, en voor
   plugins in een marketplace de `ref`/`sha` uit `marketplace.json`;
2. het archief van die commit downloaden en daaruit `marketplace.json`,
   `plugin.json` en de `SKILL.md` bestanden lezen;
3. alle plugins en skills van de bron in één transactie vervangen door het
   resultaat;
4. de skills naar Typesense sturen.

Mislukt een bron, dan blijft de vorige inhoud staan en komt de fout in
`lastError` van de bron (`GET /v1/sources`).

De harvester gebruikt geen GitHub API en geen token, dus de API rate limit
geldt niet. De commit van een branch of tag komt via het git-protocol
(`github.com/<owner>/<repo>.git/info/refs`, net als `git ls-remote`), de
bestanden uit het archief van die commit
(`codeload.github.com/<owner>/<repo>/tar.gz/<sha>`). Dat is één download per
repository per harvest. Alleen publieke repositories worden ondersteund.

Omgevingsvariabelen:

- `GITHUB_BASE`: standaard `https://github.com`.
- `GITHUB_CODELOAD_BASE`: standaard `https://codeload.github.com`.
- `DISABLE_HARVEST`: zet op `true` om de harvest-job niet te starten.

## Endpoints

| Methode | Pad                        | Omschrijving                                                |
| ------- | -------------------------- | ----------------------------------------------------------- |
| GET     | `/v1/skills`               | Skills, met filters `q`, `plugin`, `source`, `organisation` |
| GET     | `/v1/skills/filters`       | Filteropties met counts                                     |
| GET     | `/v1/skills/{id}`          | Skill met frontmatter en inhoud                             |
| GET     | `/v1/skills/{id}/skill.md` | Ruwe `SKILL.md`                                             |
| GET     | `/v1/sources`              | Bronnen met status van de laatste harvest                   |
| GET     | `/v1/sources/{id}`         | Eén bron                                                    |
| POST    | `/v1/sources`              | Bron registreren (marketplace, plugin of skill)             |
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

- **Bronnen wijzigen en verwijderen.** Er is alleen `POST /v1/sources`. Een
  `PUT` (andere organisatie of type) en `DELETE` (met de plugins en skills van
  de bron) moeten er nog bij.
- **Organisatie per bron of per plugin.** De organisatie hangt aan de bron. In
  één marketplace zitten plugins van verschillende organisaties (MinBZK, Rijks
  ICT Gilde, Geonovum); die krijgen allemaal de organisatie van de marketplace,
  tenzij de plugin los is geregistreerd met een eigen organisatie (zie
  [Eén keer in het register](#één-keer-in-het-register)).
- **Alleen GitHub.** npm-plugins en andere git-hosts (GitLab, code.overheid.nl)
  worden overgeslagen met een waarschuwing.
- **Domein en gateway.** `skills.developer.overheid.nl` en het gateway-pad
  `/skills-register/v1` zijn aannames.
- **Infra.** De test-overlay staat in
  [don-infra#513](https://github.com/developer-overheid-nl/don-infra/pull/513).
  Nog nodig: het DB-schema, de secret `skills-secrets` (alleen de `DB_*` variabelen), de
  scopes `skills:read`, `sources:read`, `sources:write`, `organisations:read` en
  `organisations:write` in Keycloak, en een productie-overlay.
- **Frontend.** Er is nog geen frontend; de Typesense-detail-URL wijst naar
  `https://skills.developer.overheid.nl/skills/{id}`.

## Licentie

Copyright (c) Geonovum. Licensed under the EUPL-1.2.

Zie ook [publiccode.yml](publiccode.yml) voor projectmetadata.
