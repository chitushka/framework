package asset

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"
)

// ViteManifestEntry соответствует записи .vite/manifest.json.
type ViteManifestEntry struct {
	File    string   `json:"file"`
	CSS     []string `json:"css"`
	Imports []string `json:"imports"`
}

// ViteManifest описывает production manifest Vite.
type ViteManifest map[string]ViteManifestEntry

// LoadViteManifest читает manifest из произвольного fs.FS.
func LoadViteManifest(filesystem fs.FS, name string) (ViteManifest, error) {
	if filesystem == nil {
		return nil, errors.New("asset: filesystem manifest не может быть nil")
	}
	if name == "" || !fs.ValidPath(name) {
		return nil, fmt.Errorf("asset: некорректный путь manifest %q", name)
	}
	payload, err := fs.ReadFile(filesystem, name)
	if err != nil {
		return nil, fmt.Errorf("asset: чтение Vite manifest %q: %w", name, err)
	}
	var manifest ViteManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return nil, fmt.Errorf("asset: разбор Vite manifest %q: %w", name, err)
	}
	if len(manifest) == 0 {
		return nil, fmt.Errorf("asset: Vite manifest %q пуст", name)
	}
	return manifest, nil
}

// Bundle строит production AssetBundle для entry, сохраняя хэшированные имена Vite.
func (manifest ViteManifest) Bundle(entry, publicPath string) (Bundle, error) {
	if manifest == nil {
		return Bundle{}, errors.New("asset: Vite manifest не задан")
	}
	if _, exists := manifest[entry]; !exists {
		return Bundle{}, fmt.Errorf("asset: entry %q отсутствует в Vite manifest", entry)
	}
	prefix, err := normalizeBaseURL(publicPath)
	if err != nil {
		return Bundle{}, err
	}
	var bundle Bundle
	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	var collect func(string, bool) error
	collect = func(name string, root bool) error {
		if visiting[name] {
			return fmt.Errorf("asset: циклический import в Vite manifest у %q", name)
		}
		if visited[name] {
			return nil
		}
		item, exists := manifest[name]
		if !exists {
			return fmt.Errorf("asset: import %q отсутствует в Vite manifest", name)
		}
		if strings.TrimSpace(item.File) == "" {
			return fmt.Errorf("asset: file для entry %q не задан", name)
		}
		visiting[name] = true
		for _, imported := range item.Imports {
			if err := collect(imported, false); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		for _, stylesheet := range item.CSS {
			bundle.Styles = append(bundle.Styles, Resource{URL: joinBase(prefix, stylesheet)})
		}
		if root {
			bundle.Scripts = append(bundle.Scripts, Resource{
				URL:        joinBase(prefix, item.File),
				Attributes: map[string]string{"type": "module"},
			})
		} else {
			bundle.ModulePreloads = append(bundle.ModulePreloads, Resource{URL: joinBase(prefix, item.File)})
		}
		return nil
	}
	if err := collect(entry, true); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

// ViteDevBundle подключает HMR client и исходный entry с Vite dev server.
func ViteDevBundle(serverURL, entry string) (Bundle, error) {
	base, err := normalizeServerURL(serverURL)
	if err != nil {
		return Bundle{}, err
	}
	if strings.TrimSpace(entry) == "" {
		return Bundle{}, errors.New("asset: Vite entry не может быть пустым")
	}
	entry = strings.TrimPrefix(entry, "/")
	module := map[string]string{"type": "module"}
	return Bundle{Scripts: []Resource{
		{URL: base + "/@vite/client", Attributes: module},
		{URL: base + "/" + entry, Attributes: module},
	}}, nil
}

func normalizeBaseURL(value string) (string, error) {
	if value == "" {
		return "/", nil
	}
	if strings.ContainsAny(value, "?#") {
		return "", fmt.Errorf("asset: public path %q содержит query или fragment", value)
	}
	if parsed, err := url.Parse(value); err != nil || (parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("asset: некорректный public path %q", value)
	}
	return strings.TrimRight(value, "/") + "/", nil
}

func normalizeServerURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("asset: некорректный URL Vite dev server %q", value)
	}
	return strings.TrimRight(value, "/"), nil
}

func joinBase(base, file string) string {
	if strings.HasPrefix(file, "/") {
		file = strings.TrimPrefix(file, "/")
	}
	if strings.Contains(base, "://") || strings.HasPrefix(base, "//") {
		return base + file
	}
	return path.Clean(base + file)
}
