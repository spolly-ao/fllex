package cycle

import "time"

// Window é a janela de renovação de um ciclo: o período em que a cobrança do
// ciclo seguinte já foi emitida e ainda pode ser paga.
//
// A janela abre antes do fim do contrato (para dar tempo de pagar sem quebra de
// serviço) e fecha depois (tolerância). Durante toda ela a cobertura mantém-se
// e a referência de pagamento continua válida, que é o que faz dos dias de
// tolerância cobertura a sério e não apenas um adiamento do corte.
type Window struct {
	// Opens é quando a cobrança do ciclo seguinte é emitida.
	Opens time.Time
	// Closes é o instante final para pagar. Passado ele sem pagamento, a
	// subscrição expira.
	Closes time.Time
}

// WindowConfig é a janela em dias, configurável por produto.
//
// Os valores por omissão (10 e 5, quinze dias no total) servem contratos
// mensais ou mais longos, em que o cliente precisa de tempo para tratar de uma
// transferência ou de uma ida ao banco. Um produto com ciclos curtos quer uma
// janela mais apertada, e diz-se com WindowConfig{LeadDays: 2, GraceDays: 4}.
type WindowConfig struct {
	// LeadDays são os dias antes do fim em que a janela abre, contando o
	// próprio dia do fim.
	LeadDays int
	// GraceDays são os dias depois do fim em que ainda se cobre e se aceita o
	// pagamento.
	GraceDays int
}

// DefaultWindow é a janela por omissão: abre 10 dias antes do fim do contrato
// e fecha 5 dias depois.
var DefaultWindow = WindowConfig{LeadDays: 10, GraceDays: 5}

// NoGrace diz explicitamente que não há tolerância nenhuma: a cobrança tem de
// estar paga no dia em que o ciclo acaba.
//
// É preciso um valor próprio porque zero significa "não definido" e recebe o
// valor por omissão. Sem isto, quem escrevesse WindowConfig{LeadDays: 10}
// ficava sem tolerância sem dar por isso, que é o género de omissão que corta o
// serviço a clientes que estavam a pagar.
const NoGrace = -1

// WithDefaults preenche os campos não definidos com os valores por omissão.
func (c WindowConfig) WithDefaults() WindowConfig {
	if c.LeadDays <= 0 {
		c.LeadDays = DefaultWindow.LeadDays
	}
	switch {
	case c.GraceDays == NoGrace:
		c.GraceDays = 0
	case c.GraceDays <= 0:
		c.GraceDays = DefaultWindow.GraceDays
	}
	return c
}

// TotalDays é a duração da janela em dias.
func (c WindowConfig) TotalDays() int {
	c = c.WithDefaults()
	return c.LeadDays + c.GraceDays
}

// For calcula a janela de renovação de um ciclo que acaba em endDate.
//
// Com os valores por omissão, um contrato que acabe a 31 abre a 22 e fecha a 5
// do mês seguinte. O lead conta o próprio dia do fim, daí o -1: lead de 10
// abre nove dias antes.
func (c WindowConfig) For(endDate time.Time) Window {
	c = c.WithDefaults()
	return Window{
		Opens:  endDate.AddDate(0, 0, -(c.LeadDays - 1)),
		Closes: EndOfDay(endDate.AddDate(0, 0, c.GraceDays)),
	}
}

// Cutoff é a data-limite a procurar quando se vai buscar candidatos a
// renovação: as subscrições cujo fim já cai dentro da antecedência da janela.
func (c WindowConfig) Cutoff(now time.Time) time.Time {
	c = c.WithDefaults()
	return now.AddDate(0, 0, c.LeadDays-1)
}

// Contains indica se o instante cai dentro da janela (limites incluídos).
func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.Opens) && !t.After(w.Closes)
}

// Expired indica se a janela já fechou sem pagamento.
func (w Window) Expired(now time.Time) bool { return now.After(w.Closes) }
