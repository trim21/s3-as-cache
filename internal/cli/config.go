package cli

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/viper"

	"github.com/trim21/s3-as-cache/internal/archive"
	"github.com/trim21/s3-as-cache/internal/cachekey"
	"github.com/trim21/s3-as-cache/internal/store"
)

// viper keys; each one reads the environment variable S3CACHE_<KEY>.
const (
	optS3             = "s3"
	optKey            = "key"
	optCompression    = "compression"
	optRegion         = "region"
	optEndpoint       = "endpoint_url"
	optAccessKey      = "access_key"
	optSecretKey      = "secret_key"
	optSessionToken   = "session_token"
	optForcePathStyle = "force_path_style"
	optCrossPlatform  = "cross_platform"
	optQuiet          = "quiet"
	optVerbose        = "verbose"
	optOverwrite      = "overwrite"
)

// envVarName is the environment variable that supplies an option.
func envVarName(option string) string {
	return envPrefix + "_" + strings.ToUpper(option)
}

// config reads the options from the environment.
type config struct {
	v *viper.Viper
}

func newConfig() *config {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.AutomaticEnv()
	v.SetDefault(optCompression, string(archive.CompressionZstd))
	return &config{v: v}
}

// str returns an option value, treating a blank variable as unset so an exported
// but empty S3CACHE_KEY still reports the missing key instead of using "".
func (c *config) str(option string) string {
	value := c.v.GetString(option)
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return value
}

// isSet reports whether the option came from the environment at all, which is
// what lets a default depend on another option.
func (c *config) isSet(option string) bool {
	return c.v.IsSet(option) && strings.TrimSpace(fmt.Sprint(c.v.Get(option))) != ""
}

func (c *config) boolean(option string) (bool, error) {
	if !c.v.IsSet(option) {
		return false, nil
	}

	raw := strings.TrimSpace(fmt.Sprint(c.v.Get(option)))
	if raw == "" {
		return false, nil
	}

	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, usagef("%s: %w", envVarName(option), err)
	}
	return parsed, nil
}

type settings struct {
	s3URL          string
	key            string
	region         string
	endpoint       string
	accessKey      string
	secretKey      string
	sessionToken   string
	forcePathStyle bool
	compression    archive.Compression
	crossPlatform  bool
	quiet          bool
	verbose        bool
}

type saveSettings struct {
	settings
	overwrite bool
}

func loadSettings(c *config) (settings, error) {
	compression, err := archive.ParseCompression(c.str(optCompression))
	if err != nil {
		return settings{}, usagef("%s: %w", envVarName(optCompression), err)
	}

	s := settings{
		s3URL:        c.str(optS3),
		key:          c.str(optKey),
		region:       c.str(optRegion),
		endpoint:     c.str(optEndpoint),
		accessKey:    c.str(optAccessKey),
		secretKey:    c.str(optSecretKey),
		sessionToken: c.str(optSessionToken),
		compression:  compression,
	}

	if s.accessKey == "" || s.secretKey == "" {
		return settings{}, usagef("%s and %s are required", envVarName(optAccessKey), envVarName(optSecretKey))
	}

	for _, option := range []struct {
		name string
		dst  *bool
	}{
		{optForcePathStyle, &s.forcePathStyle},
		{optCrossPlatform, &s.crossPlatform},
		{optQuiet, &s.quiet},
		{optVerbose, &s.verbose},
	} {
		if *option.dst, err = c.boolean(option.name); err != nil {
			return settings{}, err
		}
	}

	// S3-compatible endpoints almost always need path style addressing, so an
	// endpoint turns it on unless S3CACHE_FORCE_PATH_STYLE says otherwise.
	if !c.isSet(optForcePathStyle) && s.endpoint != "" {
		s.forcePathStyle = true
	}

	return s, nil
}

func loadSaveSettings(c *config) (saveSettings, error) {
	base, err := loadSettings(c)
	if err != nil {
		return saveSettings{}, err
	}

	s := saveSettings{settings: base}
	if s.overwrite, err = c.boolean(optOverwrite); err != nil {
		return saveSettings{}, err
	}
	return s, nil
}

// resolved holds where this invocation reads and writes, before any network call.
type resolved struct {
	cfg    store.S3Config
	comp   archive.Compression
	prefix string
}

// platform identifies the OS and architecture a cache belongs to. It is empty
// when S3CACHE_CROSS_PLATFORM is set, which makes caches interchangeable.
func (s settings) platform() string {
	if s.crossPlatform {
		return ""
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

// resolve works out where this invocation reads and writes.
func (s settings) resolve() (resolved, error) {
	cfg, err := store.ParseURL(s.s3URL)
	if err != nil {
		return resolved{}, usagef("%s: %w", envVarName(optS3), err)
	}
	cfg.Region = s.region
	cfg.Endpoint = s.endpoint
	cfg.ForcePathStyle = s.forcePathStyle
	cfg.AccessKey = s.accessKey
	cfg.SecretKey = s.secretKey
	cfg.SessionToken = s.sessionToken

	return resolved{
		cfg:    cfg,
		comp:   s.compression,
		prefix: cachekey.ObjectPrefix(cfg.Prefix, s.platform()),
	}, nil
}

// session is the store plus everything a command needs to do its work.
type session struct {
	store  store.Store
	log    *logger
	comp   archive.Compression
	prefix string
}

func openSession(opts Options, r resolved, log *logger) (*session, error) {
	openStore := opts.OpenStore
	if openStore == nil {
		openStore = func(cfg store.S3Config) (store.Store, error) {
			return store.NewS3(cfg)
		}
	}

	st, err := openStore(r.cfg)
	if err != nil {
		return nil, failuref("open store: %w", err)
	}

	return &session{store: st, log: log, comp: r.comp, prefix: r.prefix}, nil
}
