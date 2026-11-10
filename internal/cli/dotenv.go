package cli

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"
)

// LoadDotEnv carrega KEY=VALUE de um arquivo .env para o ambiente do processo.
// Variáveis já definidas não são sobrescritas; o arquivo ausente não é erro.
// Comentários (# no início ou depois de espaço) e aspas simples ou duplas são removidos.
func LoadDotEnv(path string) error {
	f, err := os.Open(path) //nolint:gosec // caminho fixo escolhido pelo binário
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		val = strings.TrimSpace(val)
		if strings.HasPrefix(val, `"`) || strings.HasPrefix(val, `'`) {
			if end := strings.IndexByte(val[1:], val[0]); end >= 0 {
				val = val[1 : end+1]
			}
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		} else if strings.HasPrefix(val, "#") {
			val = ""
		}
		if _, set := os.LookupEnv(key); set || key == "" {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return err
		}
	}
	return sc.Err()
}
