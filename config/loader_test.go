package config_test

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chitushka/framework/config"
)

type databaseConfig struct {
	Host    string        `json:"host" env:"DB_HOST" envRequired:"true"`
	Port    int           `json:"port" env:"DB_PORT" envDefault:"5432"`
	Timeout time.Duration `json:"-" env:"DB_TIMEOUT" envDefault:"5s"`
}

type applicationConfig struct {
	Debug   bool           `json:"debug" env:"APP_DEBUG"`
	Ratio   float64        `json:"ratio" env:"APP_RATIO"`
	Workers uint           `json:"workers" env:"APP_WORKERS"`
	Origins []string       `json:"origins" env:"APP_ORIGINS"`
	Address *netip.Addr    `json:"-" env:"APP_ADDRESS"`
	DB      databaseConfig `json:"db"`
}

func TestLoaderCombinesJSONEnvFileAndEnvironment(t *testing.T) {
	directory := t.TempDir()
	jsonPath := writeFile(t, directory, "config.json", `{
        "debug": false,
        "ratio": 1.25,
        "workers": 2,
        "origins": ["json.example"],
        "db": {"host": "json-db", "port": 3306}
    }`)
	envPath := writeFile(t, directory, ".env", `
        # файл окружения
        export APP_DEBUG=true
        APP_ORIGINS="one.example,two.example"
		APP_ADDRESS=192.0.2.1
        DB_HOST=env-file-db # комментарий
    `)

	var loaded applicationConfig
	loader := config.New(
		config.WithJSONFile(jsonPath),
		config.WithEnvFile(envPath),
		config.WithEnvironment(map[string]string{
			"DB_HOST":     "process-db",
			"APP_WORKERS": "8",
		}),
	)
	if err := loader.Load(&loaded); err != nil {
		t.Fatal(err)
	}

	if !loaded.Debug || loaded.Ratio != 1.25 || loaded.Workers != 8 {
		t.Fatalf("неверные значения приложения: %+v", loaded)
	}
	if loaded.DB.Host != "process-db" || loaded.DB.Port != 3306 || loaded.DB.Timeout != 5*time.Second {
		t.Fatalf("неверные значения БД: %+v", loaded.DB)
	}
	if strings.Join(loaded.Origins, "|") != "one.example|two.example" {
		t.Fatalf("неверный список: %v", loaded.Origins)
	}
	if loaded.Address == nil || loaded.Address.String() != "192.0.2.1" {
		t.Fatalf("неверный адрес: %v", loaded.Address)
	}
}

func TestLoaderAppliesJSONFilesInOrder(t *testing.T) {
	directory := t.TempDir()
	base := writeFile(t, directory, "base.json", `{"debug":false,"db":{"host":"base"}}`)
	local := writeFile(t, directory, "local.json", `{"debug":true,"db":{"host":"local"}}`)
	var loaded applicationConfig
	loader := config.New(
		config.WithJSONFile(base),
		config.WithJSONFile(local),
		config.WithEnvironment(map[string]string{}),
	)
	if err := loader.Load(&loaded); err != nil {
		t.Fatal(err)
	}
	if !loaded.Debug || loaded.DB.Host != "local" || loaded.DB.Port != 5432 {
		t.Fatalf("неверное наложение: %+v", loaded)
	}
}

func TestLoaderValidatesTargetAndRequiredValues(t *testing.T) {
	loader := config.New(config.WithEnvironment(map[string]string{}))
	if err := loader.Load(applicationConfig{}); err == nil {
		t.Fatal("ожидалась ошибка target")
	}
	var loaded applicationConfig
	if err := loader.Load(&loaded); err == nil || !strings.Contains(err.Error(), "DB_HOST") {
		t.Fatalf("ожидалась ошибка обязательной переменной, получено %v", err)
	}
}

func TestLoaderReportsSourceErrors(t *testing.T) {
	directory := t.TempDir()
	invalidJSON := writeFile(t, directory, "invalid.json", `{"unknown": true}`)
	invalidEnv := writeFile(t, directory, ".env", "INVALID LINE")

	for _, loader := range []*config.Loader{
		config.New(config.WithJSONFile(filepath.Join(directory, "missing.json"))),
		config.New(config.WithJSONFile(invalidJSON)),
		config.New(config.WithEnvFile(invalidEnv)),
	} {
		var loaded applicationConfig
		if err := loader.Load(&loaded); err == nil {
			t.Fatal("ожидалась ошибка источника")
		}
	}
}

func TestLoaderReportsConversionError(t *testing.T) {
	var loaded applicationConfig
	loader := config.New(config.WithEnvironment(map[string]string{
		"DB_HOST":     "db",
		"APP_WORKERS": "many",
	}))
	if err := loader.Load(&loaded); err == nil || !strings.Contains(err.Error(), "APP_WORKERS") {
		t.Fatalf("ожидалась ошибка преобразования, получено %v", err)
	}
}

func TestLoaderReportsTextUnmarshalerFailure(t *testing.T) {
	var loaded applicationConfig
	loader := config.New(config.WithEnvironment(map[string]string{
		"DB_HOST":     "db",
		"APP_ADDRESS": "invalid",
	}))
	if err := loader.Load(&loaded); err == nil {
		t.Fatal("ожидалась ошибка адреса")
	}
}

func writeFile(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestErrorsRemainInspectable(t *testing.T) {
	want := os.ErrNotExist
	loader := config.New(config.WithJSONFile("missing.json"))
	var loaded applicationConfig
	if err := loader.Load(&loaded); !errors.Is(err, want) {
		t.Fatalf("исходная ошибка потеряна: %v", err)
	}
}
