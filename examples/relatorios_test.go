package examples_test

import (
	"fmt"
	"strings"

	"github.com/spolly-ao/fllex/payment"
	"github.com/spolly-ao/fllex/providers/proxypay"
)

// A reconciliação compara o que o gateway diz que cobrou com o que o nosso
// lado registou. Os gateways que dão o relatório pela API implementam
// payment.StatementReader (o Stripe, pelas linhas do saldo; o MoMenu, pelas
// facturas, só em contas Pro); os que não o dão implementam
// payment.StatementImporter, e lêem o ficheiro que a equipa descarrega do
// painel deles, tal como veio do Excel.
func Example_relatorioImportadoDoPainel() {
	var gateway payment.Provider = proxypay.New(proxypay.Config{APIKey: "chave", Entity: "01234"})

	importador, ok := gateway.(payment.StatementImporter)
	fmt.Println("importa relatórios:", ok)

	ficheiro := "\xef\xbb\xbfid;reference_id;amount;datetime;invoice\r\n" +
		"901;123456789;2.500,00;16/10/2026 10:00:00;pay_A\r\n"
	linhas, err := importador.ImportStatement(strings.NewReader(ficheiro))
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, l := range linhas {
		fmt.Println(l.Type, l.ChargeRef, l.Reference, l.Amount, l.OccurredAt.Format("2006-01-02 15:04 MST"))
	}

	// Uma coluna em falta diz-se pelo nome, logo à cabeça.
	_, err = importador.ImportStatement(strings.NewReader("id;amount\n1;10\n"))
	fmt.Println(err)

	// Output:
	// importa relatórios: true
	// charge 123456789 pay_A 2500.00 AOA 2026-10-16 09:00 UTC
	// faltam colunas no relatório: reference_id, datetime
}
