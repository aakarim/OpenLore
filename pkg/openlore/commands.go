package openlore

import (
	"github.com/aakarim/go-openlore/pkg/openlore/meta"
	"github.com/aakarim/go-openlore/pkg/openlore/validation"
)

// MetaExtenderProvider is implemented by a plugin that enriches `lore meta`
// records. registerPlugin detects it and collects each extender onto the server,
// which installs them per session (buildSessionShell). This is how the okf
// plugin annotates documents with OKF conformance in `lore meta` output where
// OKF applies, so read-side discovery agrees with write-side enforcement —
// without coupling the generic `lore meta` reader (pkg/meta) to the OKF spec.
type MetaExtenderProvider interface {
	MetaExtenders() []meta.Extender
}

type MetaFilterProvider interface{ MetaFilters() []meta.Filter }

// ValidatorProvider is implemented by a plugin that contributes checks to the
// core `lore validate` command. registerPlugin collects validators onto the
// server, which installs them per session in buildSessionShell.
type ValidatorProvider interface{ Validators() []validation.Validator }
