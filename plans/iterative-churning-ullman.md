# Skills Codification Plan

## Context

This session produced extensive review feedback on Go project architecture, protobuf design, CLI patterns, and API design. The feedback should be codified into skills so it doesn't need to be repeated. This plan identifies what to update and what to create.

## Skills to Update

### 1. `organizing-go-code` — Add API Design and Constructor Patterns

The existing skill covers file naming, declaration ordering, and method placement. It's missing the API design wisdom from this session.

**Add sections:**

- **Minimal Public API**: Every package should expose only what external consumers need. Unexport types, functions, and constants that are only used within the package. If it's not used outside the package, it's private. When in doubt, start private — you can always export later.

- **Constructor Patterns**: Required parameters are positional constructor arguments. Functional options are only for truly optional configuration (things with sensible defaults). Logger is always a required parameter, never a functional option. Example: `func NewClient(logger *slog.Logger, options ...ClientOption) Client` where only `ClientWithHTTPClient` is an option (defaults to `http.DefaultClient`).

- **No Global State**: Never use `slog.Default()`, `http.DefaultClient` as implicit defaults in constructors, or `os.Getenv()` directly. Use dependency injection: loggers from `appext.Container.Logger()`, env vars from `container.Env()`, HTTP clients as explicit params.

- **File Consolidation**: Public types belong in a file named after the package (`foo.go` for package `foo`) or in files named after the type. Don't split a package across files unless there's a clear organizational reason. The financemigrate pattern: one `.go` file per package directory.

- **Section Comment Convention**: Use `// *** PRIVATE ***` to separate public and private declarations when it aids readability.

### 2. `scaffolding-go-projects` — Add Package Naming and Structure Conventions

**Add sections:**

- **Package Naming for Provider-Specific Code**: Packages wrapping a specific external service should be named after that service, not the abstract concept. Examples: `frankfurter` not `fxrate`, `ibkrflexquery` not `flexquery`. The package name tells you what provider it wraps.

- **Package Dependency Layering**: Enforce strict import ordering: `cmd → internal/{app} → internal/pkg → internal/standard`. Standard packages (`internal/standard/x*`) extend stdlib only. Pkg packages (`internal/pkg`) are generic. App packages (`internal/{app}`) are app-specific and prefixed (e.g., `ibctldownload`).

- **Leaning into `buf.build/go/app`**: Use `appext.Container` for config/data/cache directory paths, logger, and environment variables. Use `ConfigDirPath()` / `DataDirPath()` instead of configurable paths. Use `container.Env()` instead of `os.Getenv()`. Use `container.Logger()` instead of creating loggers.

## New Skills to Create

### 3. NEW: `designing-protobuf-messages` (or update `buf-standards:protobuf`)

**Description**: Use when designing protobuf messages for data storage or APIs, defining field types, adding validation, or reviewing proto file structure.

**Content:**

- **Protovalidate Everywhere**: Every proto file should import `buf/validate/validate.proto`. Required fields use `(buf.validate.field).required = true`. Enums use `(buf.validate.field).enum.not_in = 0` to reject unspecified values.

- **Use Enums Over Strings for Closed Sets**: Don't use strings for fields with a known set of values (e.g., buy/sell). Define a proper enum with `TYPE_UNSPECIFIED = 0` and specific values. Follow buf naming: `ENUM_NAME_VALUE_NAME` (e.g., `TRADE_SIDE_BUY`). Handle the unspecified case exhaustively in Go switch statements.

- **Currency Codes**: Use `string.pattern = "^[A-Z]{3}$"` for ISO 4217 currency codes, not just `string.len = 3`.

- **Message-Level Consistency via CEL**: When a message has fields that must be consistent with each other, use `(buf.validate.message).cel` constraints to enforce it. Examples: sub-message fields that must match a top-level field, dates that must be ordered, quantities that must agree. Use `!has()` guards for optional fields. Give each constraint a descriptive `id` and `message`.

- **No Wrapper Messages for Repeated Types**: Don't create `Foos { repeated Foo foos = 1; }` just for JSON serialization. Store newline-separated proto JSON (one message per line). Provide `WriteMessagesJSON`/`ReadMessagesJSON` helpers.

- **Use Standard Types**: Use `google.protobuf.Timestamp` for timestamps, not strings. Use `standard.money.v1.Money` (units + micros) for monetary values, not strings. Use `standard.time.v1.Date` for dates.

- **Decimal Values as Units/Micros**: Don't store decimal numbers as strings. Use the units/micros pattern (int64 units + int64 micros with `gte = -999999, lte = 999999`). This applies to exchange rates, prices, or any decimal value.

- **File Naming**: Proto files use `snake_case.proto` (e.g., `exchange_rate.proto` not `exchangerate.proto`).

- **Dynamic vs Stored Fields**: Don't store fields that are dynamic/computed relative to the current date (e.g., `long_term` based on holding period). Compute them at display time with an `--as-of` flag defaulting to today.

### 4. NEW: `designing-go-cli-commands`

**Description**: Use when building CLI commands using `buf.build/go/app`, wiring command trees, handling configuration, or designing command flags and error messages.

**Content:**

- **Configuration via `appext.Container`**: Use `container.ConfigDirPath()` for config file location, `container.DataDirPath()` for data storage. Don't add `--config` flags for file paths. Config file is always `config.yaml` in the config dir.

- **Config Init / Edit / Validate Pattern**: Provide `config init` (create with template, print path), `config edit` (open in `$EDITOR`, create if missing, print path), `config validate` (read + validate). Init errors if file exists. Edit creates if it doesn't exist.

- **Error Messages That Guide Users**: When config is missing, tell them what to run: `configuration file not found at %s, run "ibctl config init" to create one`. Don't just say "file not found".

- **Secrets via Environment Variables**: API keys and tokens come from env vars (read via `container.Env()`), never from config files. Document the env var name in the config template as a comment and in the README.

- **Root Command Help**: Include config and data directory paths in the root command's `Long` description so users can find them from `--help`.

- **Structured Return Types Over Raw Bytes**: API clients should return parsed, structured data, not raw `[]byte`. Parse internally, return typed structs. Document the API flow in the package doc comment.

- **Command Wiring**: Commands construct their dependencies explicitly in the `run` function. Extract logger from `container.Logger()`, construct clients with required params, pass everything to the domain package. No global state, no defaults.

## Summary

| Skill | Action | Key Topics |
|-------|--------|------------|
| `organizing-go-code` | Update | Minimal API, constructors, no global state, file consolidation |
| `scaffolding-go-projects` | Update | Provider naming, dependency layering, app-go patterns |
| `designing-protobuf-messages` | Create | Protovalidate, enums, currency, Money consistency, no wrappers, units/micros |
| `designing-go-cli-commands` | Create | app-go config, init/edit/validate, error messages, secrets, structured returns |
