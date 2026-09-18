package i18n

import (
	"fmt"
	"os"
	"strings"
)

const (
	LocaleEnv       = "BOT_LOCALE"
	LegacyLocaleEnv = "RANSOMWARE_BOT_LOCALE"
	defaultLocale   = "en"
)

type Messages struct {
	locale string
}

func FromArgsAndEnv(args []string) Messages {
	return ForLocale(localeFromArgs(args, localeFromEnv()))
}

func Default() Messages {
	return Messages{locale: defaultLocale}
}

func ForLocale(locale string) Messages {
	locale = normalizeLocale(locale)
	return Messages{locale: locale}
}

func (m Messages) Locale() string {
	return m.locale
}

func (m Messages) T(key string) string {
	if value, ok := catalogForLocale(m.locale)[key]; ok {
		return value
	}
	if value, ok := englishMessages[key]; ok {
		return value
	}
	return key
}

func (m Messages) Tf(key string, args ...any) string {
	return fmt.Sprintf(m.T(key), args...)
}

func localeFromEnv() string {
	if locale := strings.TrimSpace(os.Getenv(LocaleEnv)); locale != "" {
		return locale
	}
	return strings.TrimSpace(os.Getenv(LegacyLocaleEnv))
}

func localeFromArgs(args []string, fallback string) string {
	for i, arg := range args {
		if arg == "--locale" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return fallback
		}
		if strings.HasPrefix(arg, "--locale=") {
			return strings.TrimPrefix(arg, "--locale=")
		}
	}
	return fallback
}

func normalizeLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	locale = strings.ReplaceAll(locale, "_", "-")
	if locale == "" {
		return defaultLocale
	}
	switch {
	case locale == "de" || strings.HasPrefix(locale, "de-"):
		return "de"
	default:
		return defaultLocale
	}
}

func catalogForLocale(locale string) map[string]string {
	switch normalizeLocale(locale) {
	case "de":
		return germanMessages
	default:
		return englishMessages
	}
}

var englishMessages = map[string]string{
	"cli.flag.config_dir":               "Directory containing configuration files",
	"cli.flag.data_dir":                 "Directory for persistent data (overrides config and DATA_DIR env)",
	"cli.flag.locale":                   "Locale for CLI help and status messages (for example en or de)",
	"cli.flag.version":                  "Show version information",
	"cli.flag.check_config":             "Validate configuration and exit",
	"cli.flag.healthcheck":              "Validate runtime health and exit",
	"cli.flag.list_dead_letter":         "List terminal webhook delivery failures and exit",
	"cli.flag.dry_run":                  "Dry-run mode: format messages and log them without sending",
	"cli.flag.accept_destination_remap": "Accept a changed webhook-endpoint-to-destination-ID mapping once and rewrite destinations.json",

	"cli.version":                 "Ransomware News Bot v%s (commit %s, built %s)",
	"cli.error":                   "Error: %v\n",
	"cli.error_plain":             "Error %v\n",
	"cli.error_bootstrap_logger":  "Error setting up bootstrap logger: %v\n",
	"cli.error_logger":            "Error setting up logger: %v\n",
	"cli.healthcheck_failed":      "Healthcheck failed: %v\n",
	"cli.healthcheck_valid":       "Healthcheck valid",
	"cli.dead_letter_error":       "Error listing dead-letter items: %v\n",
	"cli.config_invalid":          "Configuration invalid: %v\n",
	"cli.config_invalid_data_dir": "Configuration invalid: data_dir: %v\n",
	"cli.config_warning_no_targets": "Configuration warning: no delivery channels enabled; " +
		"enable at least one discord_webhooks.* or slack_webhooks.* target to send alerts",
	"cli.config_warning_shared_webhook_url": "Configuration warning: webhook endpoints %s share one webhook URL; " +
		"where their filters overlap the same alert is posted to that channel once per endpoint",
	"cli.config_warning_manifest_unreadable": "Configuration warning: destinations.json could not be read (%s); " +
		"the destination endpoint-order check was skipped for this run",
	"cli.config_valid": "Configuration valid",
	"cli.destination_remap": "Destination remap detected (%s): %s. Stored dedup markers, retry-queue rows and dead letters " +
		"are keyed by the destination ID, so starting with this configuration would silently suppress alerts for one " +
		"endpoint and deliver another endpoint's queued messages to the wrong webhook. Restore the previous endpoint " +
		"order (append new endpoints at the end of url/urls/targets, delete only from the end), or start once with " +
		"--accept-destination-remap to accept the new mapping and rewrite %s.\n",

	"destination_remap.kind.reorder":     "endpoints reordered",
	"destination_remap.kind.insert":      "endpoint inserted before an existing one",
	"destination_remap.kind.delete":      "endpoint deleted before an existing one",
	"destination_remap.kind.url_changed": "endpoint URL changed",

	"health.data_dir":                        "Data dir: %s\n",
	"health.destination_manifest_unreadable": "Destination manifest unreadable (%s); the endpoint-order check was skipped for this run\n",
	"health.retry_queue_items":               "Retry queue items: %d\n",
	"health.dead_letter_items":               "Dead-letter items: %d\n",
	"health.readiness_marker":                "Readiness marker: %s\n",
	"health.api_auth_suspended":              "ransomware API polling suspended after HTTP %d since %s: %s; it lifts on an api_key/api_base_url change, on restart, or at the next automatic re-probe",
	"health.data_dir_missing":                "data_dir %s does not exist; this check never creates it (possibly an unmounted volume)",
	"health.rss_all_feeds_failed":            "all %d enabled RSS feed(s) failed their last poll; last error: %s",
	"health.rss_no_recent_success":           "no enabled RSS feed has succeeded since %s (allowed: %s = 3 x rss_poll_interval); %d of %d feeds report an error",
	"health.rss_feeds_degraded":              "RSS feeds degraded: %d of %d enabled feeds failed their last poll; newest successful poll %s\n",
	"health.rss_never":                       "never",
	"health.rss_never_polled":                "RSS is enabled (%d feed(s) configured) but no poll attempt has been recorded since the scheduler finished starting; check the data_dir volume mount and its write permissions",
	"health.rss_budget_overrun":              "RSS is enabled (%d feed(s) configured) but the last completed poll pass exhausted its rss_check_timeout (%s) before any feed answered; raise rss_check_timeout above rss_worker_timeout (%s) so feeds have time to complete",
	"health.poller_wedged":                   "the %s poller has completed no poll pass for %s (last completed %s); the allowance is %s, three times its poll interval plus its check timeout, so the poller is wedged rather than idle",
	"health.destination_remap": "destination remap pending (%s): %s; the bot refuses to start until the previous " +
		"endpoint order is restored or it is started once with --accept-destination-remap",
	"health.data_dir_lock_unsupported": "Note: this data dir's filesystem does not support file locking (ENOLCK/EINVAL); " +
		"nothing prevents a second ransomware-bot instance from writing the same data_dir, and file logging falls " +
		"back to stdout only (bot.log stays empty) for the same reason. Move data_dir to a filesystem that supports " +
		"locking; on Unraid, use /mnt/cache or a disk share, not /mnt/user.\n",

	"dead_letter.items":          "Dead-letter items: %d\n",
	"dead_letter.data_dir":       "Data dir: %s\n",
	"dead_letter.untitled":       "(untitled)",
	"dead_letter.key":            "   key: %s\n",
	"dead_letter.destination_id": "   destination_id: %s\n",
	"dead_letter.dead_at":        "   dead_at: %s\n",
	"dead_letter.reason":         "   reason: %s\n",
	"dead_letter.retry_count":    "   retry_count: %d\n",
	"dead_letter.last_error":     "   last_error: %s\n",
	"dead_letter.replay_payload": "   replay_payload: yes",
}

var germanMessages = map[string]string{
	"cli.flag.config_dir":               "Verzeichnis mit Konfigurationsdateien",
	"cli.flag.data_dir":                 "Verzeichnis fuer persistente Daten (ueberschreibt Config und DATA_DIR)",
	"cli.flag.locale":                   "Locale fuer CLI-Hilfe und Statusmeldungen (zum Beispiel en oder de)",
	"cli.flag.version":                  "Versionsinformationen anzeigen",
	"cli.flag.check_config":             "Konfiguration pruefen und beenden",
	"cli.flag.healthcheck":              "Laufzeitstatus pruefen und beenden",
	"cli.flag.list_dead_letter":         "Terminal fehlgeschlagene Webhook-Zustellungen auflisten und beenden",
	"cli.flag.dry_run":                  "Dry-run: Nachrichten formatieren und loggen, aber nicht senden",
	"cli.flag.accept_destination_remap": "Geaenderte Zuordnung von Webhook-Endpunkt zu Ziel-ID einmalig akzeptieren und destinations.json neu schreiben",

	"cli.version":                   "Ransomware News Bot v%s (Commit %s, gebaut %s)",
	"cli.error":                     "Fehler: %v\n",
	"cli.error_plain":               "Fehler %v\n",
	"cli.error_bootstrap_logger":    "Fehler beim Einrichten des Bootstrap-Loggers: %v\n",
	"cli.error_logger":              "Fehler beim Einrichten des Loggers: %v\n",
	"cli.healthcheck_failed":        "Healthcheck fehlgeschlagen: %v\n",
	"cli.healthcheck_valid":         "Healthcheck gueltig",
	"cli.dead_letter_error":         "Fehler beim Auflisten der Dead-Letter-Eintraege: %v\n",
	"cli.config_invalid":            "Konfiguration ungueltig: %v\n",
	"cli.config_invalid_data_dir":   "Konfiguration ungueltig: data_dir: %v\n",
	"cli.config_warning_no_targets": "Konfigurationswarnung: keine Zustellkanaele aktiviert; aktiviere mindestens ein discord_webhooks.* oder slack_webhooks.* Ziel, um Alerts zu senden",
	"cli.config_warning_shared_webhook_url": "Konfigurationswarnung: die Webhook-Ziele %s verwenden dieselbe Webhook-URL; " +
		"wo sich ihre Filter ueberschneiden, wird derselbe Alert pro Ziel einmal in diesen Kanal gepostet",
	"cli.config_warning_manifest_unreadable": "Konfigurationswarnung: destinations.json konnte nicht gelesen werden (%s); " +
		"die Endpunkt-Reihenfolge-Pruefung wurde fuer diesen Lauf uebersprungen",
	"cli.config_valid": "Konfiguration gueltig",
	"cli.destination_remap": "Ziel-Neuzuordnung erkannt (%s): %s. Gespeicherte Dedup-Marker, Retry-Eintraege und " +
		"Dead-Letter-Eintraege sind an die Ziel-ID gebunden; ein Start mit dieser Konfiguration wuerde Alerts eines " +
		"Endpunkts stillschweigend unterdruecken und die eingereihten Nachrichten eines anderen Endpunkts an den " +
		"falschen Webhook zustellen. Stelle die bisherige Reihenfolge der Endpunkte wieder her (neue Endpunkte nur " +
		"am Ende von url/urls/targets anhaengen, nur am Ende loeschen) oder starte einmalig mit " +
		"--accept-destination-remap, um die neue Zuordnung zu akzeptieren und %s neu zu schreiben.\n",

	"destination_remap.kind.reorder":     "Endpunkte umsortiert",
	"destination_remap.kind.insert":      "Endpunkt vor einem bestehenden eingefuegt",
	"destination_remap.kind.delete":      "Endpunkt vor einem bestehenden geloescht",
	"destination_remap.kind.url_changed": "Endpunkt-URL geaendert",

	"health.data_dir":                        "Datenverzeichnis: %s\n",
	"health.destination_manifest_unreadable": "Ziel-Manifest nicht lesbar (%s); die Endpunkt-Reihenfolge-Pruefung wurde fuer diesen Lauf uebersprungen\n",
	"health.retry_queue_items":               "Retry-Queue-Eintraege: %d\n",
	"health.dead_letter_items":               "Dead-Letter-Eintraege: %d\n",
	"health.readiness_marker":                "Readiness-Marker: %s\n",
	"health.api_auth_suspended":              "Ransomware-API-Abfrage nach HTTP %d seit %s ausgesetzt: %s; sie endet durch Aenderung von api_key/api_base_url, durch Neustart oder beim naechsten automatischen Neuversuch",
	"health.data_dir_missing":                "Datenverzeichnis %s existiert nicht; diese Pruefung legt es nicht an (moeglicherweise ein nicht eingehaengtes Volume)",
	"health.rss_all_feeds_failed":            "alle %d aktivierten RSS-Feeds sind beim letzten Abruf fehlgeschlagen; letzter Fehler: %s",
	"health.rss_no_recent_success":           "kein aktivierter RSS-Feed war seit %s erfolgreich (erlaubt: %s = 3 x rss_poll_interval); %d von %d Feeds melden einen Fehler",
	"health.rss_feeds_degraded":              "RSS-Feeds beeintraechtigt: %d von %d aktivierten Feeds sind beim letzten Abruf fehlgeschlagen; letzter erfolgreicher Abruf %s\n",
	"health.rss_never":                       "nie",
	"health.rss_never_polled":                "RSS ist aktiviert (%d Feed(s) konfiguriert), aber seit dem Startabschluss des Schedulers wurde kein Abrufversuch aufgezeichnet; Mount und Schreibrechte von data_dir pruefen",
	"health.rss_budget_overrun":              "RSS ist aktiviert (%d Feed(s) konfiguriert), aber der letzte abgeschlossene Abrufdurchlauf hat sein rss_check_timeout (%s) ausgeschoepft, bevor ein Feed geantwortet hat; rss_check_timeout ueber rss_worker_timeout (%s) anheben, damit Feeds Zeit zum Abschliessen haben",
	"health.poller_wedged":                   "der %s-Poller hat seit %s keinen Abrufdurchlauf abgeschlossen (letzter Durchlauf %s); erlaubt sind %s, das Dreifache aus Poll-Intervall plus Check-Timeout, der Poller haengt also, statt nur untaetig zu sein",
	"health.destination_remap": "Ziel-Neuzuordnung ausstehend (%s): %s; der Bot startet nicht, bis die " +
		"bisherige Reihenfolge wiederhergestellt ist oder er einmalig mit --accept-destination-remap gestartet wird",
	"health.data_dir_lock_unsupported": "Hinweis: Das Dateisystem dieses Datenverzeichnisses unterstuetzt keine Dateisperren " +
		"(ENOLCK/EINVAL); nichts verhindert, dass eine zweite ransomware-bot-Instanz dasselbe data_dir beschreibt, und " +
		"aus demselben Grund faellt die Datei-Protokollierung auf reine Standardausgabe zurueck (bot.log bleibt leer). " +
		"Verschiebe data_dir auf ein Dateisystem mit Sperrunterstuetzung; unter Unraid /mnt/cache oder eine " +
		"Disk-Freigabe statt /mnt/user verwenden.\n",

	"dead_letter.items":          "Dead-Letter-Eintraege: %d\n",
	"dead_letter.data_dir":       "Datenverzeichnis: %s\n",
	"dead_letter.untitled":       "(ohne Titel)",
	"dead_letter.key":            "   Schluessel: %s\n",
	"dead_letter.destination_id": "   Ziel-ID: %s\n",
	"dead_letter.dead_at":        "   tot_seit: %s\n",
	"dead_letter.reason":         "   Grund: %s\n",
	"dead_letter.retry_count":    "   Retry-Anzahl: %d\n",
	"dead_letter.last_error":     "   letzter_fehler: %s\n",
	"dead_letter.replay_payload": "   replay_payload: ja",
}
