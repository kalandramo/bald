package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// cfgWith 帮助函数：构造只含配置源子配置的契约。
func cfgWith(c *bootstrapv1.Config) *bootstrapv1.BootstrapConfig {
	return &bootstrapv1.BootstrapConfig{Config: c}
}

func TestEnvProvider(t *testing.T) {
	t.Setenv("BOOTSTRAP_TEST_DEFAULT", "from-env")
	cfg := cfgWith(&bootstrapv1.Config{
		Env: &bootstrapv1.Config_Env{
			Prefix: "BOOTSTRAP_TEST_",
			Key:    "DEFAULT",
		},
	})

	p := EnvProvider()
	l, closer, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("EnvProvider() = %v, want nil", err)
	}
	if closer != nil {
		t.Fatal("EnvProvider closer != nil, want nil (env holds no resources)")
	}
	if l.Name != "" {
		t.Fatalf("layer Name = %q, want empty (Build fills registered name)", l.Name)
	}

	data, err := l.Reader.Load(context.Background(), "")
	if err != nil || string(data) != "from-env" {
		t.Fatalf("Load() = (%q, %v), want (%q, nil)", data, err, "from-env")
	}
}

func TestEnvProvider_Unconfigured(t *testing.T) {
	p := EnvProvider()
	l, closer, err := p(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("EnvProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

func TestFileProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgWith(&bootstrapv1.Config{
		File: &bootstrapv1.Config_File{Path: path},
	})

	p := FileProvider()
	l, closer, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("FileProvider() = %v, want nil", err)
	}
	if closer == nil {
		t.Fatal("FileProvider closer == nil, want non-nil")
	}
	defer closer()

	// 格式从扩展名推断（契约 format 未声明）。
	if l.Format != "yaml" {
		t.Fatalf("layer Format = %q, want yaml (inferred from extension)", l.Format)
	}
	if l.Watch {
		t.Fatal("layer Watch = true, want false (contract watch unset)")
	}

	data, err := l.Reader.Load(context.Background(), "")
	if err != nil || string(data) != "from-file\n" {
		t.Fatalf("Load() = (%q, %v), want (%q, nil)", data, err, "from-file\n")
	}
}

// TestFileProvider_WatchAndFormat：契约 format 优先于扩展名推断；watch 透传为层 Watch。
func TestFileProvider_WatchAndFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgWith(&bootstrapv1.Config{
		File: &bootstrapv1.Config_File{Path: path, Format: "json", Watch: true},
	})

	l, closer, err := FileProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("FileProvider() = %v, want nil", err)
	}
	defer closer()
	if l.Format != "json" {
		t.Fatalf("layer Format = %q, want json (contract wins)", l.Format)
	}
	if !l.Watch {
		t.Fatal("layer Watch = false, want true (contract watch=true)")
	}
}

// TestFileProvider_MissingPath 报错路径：配置了 file 源但未给路径，file.New fail-fast。
func TestFileProvider_MissingPath(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{File: &bootstrapv1.Config_File{}})

	l, closer, err := FileProvider()(context.Background(), cfg)
	if err == nil {
		t.Fatal("FileProvider(empty path) = nil error, want error")
	}
	if l != nil || closer != nil {
		t.Fatalf("FileProvider(empty path) = (layer=%v, closer!=nil: %t), want (nil, nil)", l, closer != nil)
	}
}

func TestFileProvider_Unconfigured(t *testing.T) {
	p := FileProvider()
	l, closer, err := p(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("FileProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

// TestBuild_Integration 端到端装配：注册序产出层顺序（首=最高优先级），
// 未配置源跳过，层 Reader 直读契约源。
func TestBuild_Integration(t *testing.T) {
	t.Setenv("BOOTSTRAP_TEST_DEFAULT", "from-env")

	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := cfgWith(&bootstrapv1.Config{
		Env:  &bootstrapv1.Config_Env{Prefix: "BOOTSTRAP_TEST_", Key: "DEFAULT"},
		File: &bootstrapv1.Config_File{Path: path},
	})

	t.Run("env registered first is highest priority", func(t *testing.T) {
		r := NewRegistry()
		r.MustRegister("env", EnvProvider())
		r.MustRegister("file", FileProvider())

		layers, cleanup, err := r.Build(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Build() = %v, want nil", err)
		}
		defer cleanup()

		if len(layers) != 2 {
			t.Fatalf("len(layers) = %d, want 2", len(layers))
		}
		if layers[0].Name != "env" || layers[1].Name != "file" {
			t.Fatalf("layer names = [%q %q], want [env file] (registration order)", layers[0].Name, layers[1].Name)
		}
		data, err := layers[0].Reader.Load(context.Background(), "")
		if err != nil || string(data) != "from-env" {
			t.Fatalf("env layer Load() = (%q, %v), want (%q, nil)", data, err, "from-env")
		}
	})

	t.Run("file only", func(t *testing.T) {
		r := NewRegistry()
		r.MustRegister("file", FileProvider())

		layers, cleanup, err := r.Build(context.Background(), cfg)
		if err != nil {
			t.Fatalf("Build() = %v, want nil", err)
		}
		defer cleanup()

		if len(layers) != 1 {
			t.Fatalf("len(layers) = %d, want 1 (unconfigured env skipped)", len(layers))
		}
		if layers[0].Name != "file" {
			t.Fatalf("layer name = %q, want file", layers[0].Name)
		}
	})
}
