// Package phone normaliza números de telemóvel para o formato que os gateways
// de pagamento angolanos exigem.
//
// O Multicaixa Express só aceita 244XXXXXXXXX (só dígitos) e devolve um erro
// opaco a tudo o resto, por isso a validação é feita antes de qualquer chamada
// de rede: um número mal escrito nunca chega ao gateway.
package phone

import (
	"errors"
	"strings"
)

// ErrInvalidAO indica que o número não é um telemóvel angolano válido no
// formato que o Multicaixa Express exige (244 seguido de 9XXXXXXXX).
var ErrInvalidAO = errors.New("phone: número de telemóvel inválido para Multicaixa Express")

// countryCodeAO é o indicativo de Angola.
const countryCodeAO = "244"

// NormalizeAO converte um número para o formato do Multicaixa Express
// (244XXXXXXXXX, só dígitos): remove "+", espaços, hífenes e parênteses, tira
// o prefixo internacional "00" e acrescenta o 244 a um número nacional de nove
// dígitos.
//
// Assim, "+244 921 234 567", "00244921234567" e "921 234 567" ficam todos
// "244921234567".
func NormalizeAO(s string) string {
	d := Digits(s)
	d = strings.TrimPrefix(d, "00")
	if len(d) == 9 && d[0] == '9' {
		d = countryCodeAO + d
	}
	return d
}

// ValidAO indica se o número, depois de normalizado, é um telemóvel angolano
// completo: 244 seguido de 9 e mais oito dígitos.
func ValidAO(s string) bool {
	d := NormalizeAO(s)
	return len(d) == 12 && strings.HasPrefix(d, countryCodeAO+"9")
}

// CheckAO normaliza e valida de uma vez. Devolve o número pronto a enviar ou
// [ErrInvalidAO].
func CheckAO(s string) (string, error) {
	d := NormalizeAO(s)
	if len(d) != 12 || !strings.HasPrefix(d, countryCodeAO+"9") {
		return "", ErrInvalidAO
	}
	return d, nil
}

// FormatAODDS devolve o número no formato "+244-XXXXXXXXX" que o Proxypay DDS
// exige no campo mobile dos mandatos. Um número que não seja angolano válido
// devolve string vazia, porque enviar lixo faz o registo do mandato falhar.
func FormatAODDS(s string) string {
	d, err := CheckAO(s)
	if err != nil {
		return ""
	}
	return "+" + countryCodeAO + "-" + d[3:]
}

// Digits devolve apenas os dígitos da string.
func Digits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// SameAO compara dois números ignorando formatação e a presença do indicativo.
// É o que permite correlacionar um telemóvel guardado como "923456789" com o
// "244923456789" que o gateway devolve na factura, na reconciliação do
// Multicaixa Express.
func SameAO(a, b string) bool {
	da, db := Digits(a), Digits(b)
	if da == "" || db == "" {
		return false
	}
	if da == db {
		return true
	}
	da = strings.TrimPrefix(da, countryCodeAO)
	db = strings.TrimPrefix(db, countryCodeAO)
	return da != "" && da == db
}
