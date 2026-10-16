// Package relatorio lê os relatórios em CSV que os gateways deixam
// descarregar dos painéis deles, para os providers que não dão o relatório
// pela API ([payment.StatementImporter]).
//
// Os ficheiros que chegam assim foram abertos e gravados por alguém, quase
// sempre num Excel: vêm com BOM, com ponto e vírgula em vez de vírgula, com as
// colunas noutra ordem ou com outra capitalização. Por isso lê-se pelo nome da
// coluna e nunca pela posição, aceita-se os dois separadores, e uma coluna
// obrigatória em falta é um erro com o nome dela, logo à cabeça.
package relatorio

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrVazio é um relatório sem cabeçalho.
var ErrVazio = errors.New("relatório vazio: falta a linha do cabeçalho")

// Tabela é um relatório lido: o cabeçalho e as linhas.
type Tabela struct {
	colunas map[string]int
	linhas  [][]string
}

// Ler lê o CSV inteiro. O separador é o que aparecer mais vezes no cabeçalho,
// entre a vírgula e o ponto e vírgula.
func Ler(r io.Reader) (*Tabela, error) {
	dados, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("relatório ilegível: %w", err)
	}
	dados = bytes.TrimPrefix(dados, []byte("\xef\xbb\xbf"))
	primeira, _, _ := bufio.NewReader(bytes.NewReader(dados)).ReadLine()
	if strings.TrimSpace(string(primeira)) == "" {
		return nil, ErrVazio
	}
	leitor := csv.NewReader(bytes.NewReader(dados))
	leitor.FieldsPerRecord = -1
	if strings.Count(string(primeira), ";") > strings.Count(string(primeira), ",") {
		leitor.Comma = ';'
	}
	registos, err := leitor.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("relatório ilegível: %w", err)
	}
	t := &Tabela{colunas: map[string]int{}}
	for i, nome := range registos[0] {
		t.colunas[normalizar(nome)] = i
	}
	for _, l := range registos[1:] {
		if len(l) == 1 && strings.TrimSpace(l[0]) == "" {
			continue
		}
		t.linhas = append(t.linhas, l)
	}
	return t, nil
}

func normalizar(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Exigir confirma que cada nome tem uma coluna (basta um dos sinónimos de
// cada grupo, separados por "|"), e diz quais faltam.
func (t *Tabela) Exigir(grupos ...string) error {
	var faltam []string
	for _, g := range grupos {
		if !t.Tem(strings.Split(g, "|")...) {
			faltam = append(faltam, strings.Split(g, "|")[0])
		}
	}
	if len(faltam) > 0 {
		return fmt.Errorf("faltam colunas no relatório: %s", strings.Join(faltam, ", "))
	}
	return nil
}

// Tem diz se alguma das colunas existe.
func (t *Tabela) Tem(nomes ...string) bool {
	for _, n := range nomes {
		if _, ok := t.colunas[normalizar(n)]; ok {
			return true
		}
	}
	return false
}

// Linhas é quantas linhas de dados o relatório tem.
func (t *Tabela) Linhas() int { return len(t.linhas) }

// Valor devolve o campo da primeira coluna que existir, sem espaços à volta.
// Vazio quando nenhuma existe, ou quando a linha é mais curta.
func (t *Tabela) Valor(linha int, nomes ...string) string {
	for _, n := range nomes {
		i, ok := t.colunas[normalizar(n)]
		if !ok {
			continue
		}
		if i < len(t.linhas[linha]) {
			return strings.TrimSpace(t.linhas[linha][i])
		}
		return ""
	}
	return ""
}

// Erro diz em que linha do ficheiro está o problema. A linha 1 é o cabeçalho,
// e é assim que quem abre o ficheiro a conta.
func Erro(linha int, formato string, args ...any) error {
	return fmt.Errorf("linha %d do relatório: %s", linha+2, fmt.Sprintf(formato, args...))
}

// Data lê as datas que os painéis escrevem: ISO com e sem fuso, com espaço ou
// com T, e a forma portuguesa dd/mm/aaaa. Sem fuso, é a hora de Angola
// (UTC+1), que é a dos painéis dos gateways angolanos.
func Data(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	angola := time.FixedZone("WAT", 3600)
	for _, l := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05", "02/01/2006 15:04:05", "02/01/2006 15:04", "2006-01-02", "02/01/2006"} {
		if t, err := time.ParseInLocation(l, s, angola); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
