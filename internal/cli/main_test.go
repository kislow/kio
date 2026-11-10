package cli

import (
	"os"
	"testing"

	"github.com/kislow/kio/internal/gitisolate"
)

func TestMain(m *testing.M) { os.Exit(gitisolate.Main(m.Run)) }
