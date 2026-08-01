package cycle

import (
	"testing"
	"time"

	"github.com/spolly-ao/fllex/money"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
}

func TestAddMonthsTruncatesToLastDay(t *testing.T) {
	tests := []struct {
		in     time.Time
		months int
		want   time.Time
	}{
		// O AddDate do Go daria 1 de Maio; um mês depois de 31 de Março é 30 de
		// Abril, que é o que qualquer pessoa entende.
		{date(2025, time.March, 31), 1, date(2025, time.April, 30)},
		// O AddDate daria 3 de Março; aqui dá o último dia de Fevereiro.
		{date(2025, time.January, 31), 1, date(2025, time.February, 28)},
		{date(2024, time.January, 31), 1, date(2024, time.February, 29)}, // ano bissexto
		{date(2025, time.January, 15), 1, date(2025, time.February, 15)},
		{date(2025, time.December, 15), 1, date(2026, time.January, 15)},
		{date(2025, time.March, 31), -1, date(2025, time.February, 28)},
		{date(2025, time.June, 15), 12, date(2026, time.June, 15)},
	}
	for _, tt := range tests {
		if got := AddMonths(tt.in, tt.months); !got.Equal(tt.want) {
			t.Errorf("AddMonths(%s, %d) = %s, queria %s",
				tt.in.Format("2006-01-02"), tt.months,
				got.Format("2006-01-02"), tt.want.Format("2006-01-02"))
		}
	}
}

func TestAddMonthsAnchoredDoesNotDrift(t *testing.T) {
	// Uma subscrição assinada a 31 é cobrada a 28 em Fevereiro e volta ao 31 em
	// Março. Somar mês a mês sem âncora prendia-a no 28 para o resto do
	// contrato.
	anchor := date(2025, time.January, 31)
	current := anchor
	for i := 0; i < 12; i++ {
		current = AddMonthsAnchored(current, 1, anchor.Day())
	}
	if want := date(2026, time.January, 31); !current.Equal(want) {
		t.Errorf("doze meses a partir de 31 de Janeiro = %s, queria %s",
			current.Format("2006-01-02"), want.Format("2006-01-02"))
	}

	// Sem âncora, a deriva é visível já ao segundo mês.
	drifted := AddMonths(AddMonths(anchor, 1), 1)
	if want := date(2025, time.March, 28); !drifted.Equal(want) {
		t.Errorf("sem âncora, dois meses deram %s, queria %s (a deriva que a âncora corrige)",
			drifted.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestNthPeriodEndFromAnchor(t *testing.T) {
	anchor := date(2025, time.January, 31)
	tests := []struct {
		n    int
		want time.Time
	}{
		{1, date(2025, time.February, 28)},
		{2, date(2025, time.March, 31)},
		{3, date(2025, time.April, 30)},
		{12, date(2026, time.January, 31)},
	}
	for _, tt := range tests {
		if got := NthPeriodEnd(anchor, 1, tt.n); !got.Equal(tt.want) {
			t.Errorf("ciclo %d = %s, queria %s", tt.n,
				got.Format("2006-01-02"), tt.want.Format("2006-01-02"))
		}
	}
}

func TestParseInterval(t *testing.T) {
	tests := []struct {
		in   string
		want Interval
	}{
		{"monthly", Monthly}, {"month", Monthly}, {"mensal", Monthly},
		{"yearly", Yearly}, {"year", Yearly}, {"anual", Yearly},
		{"trimestral", Quarterly}, {"semestral", Semiannual}, {"weekly", Weekly},
		{"", Monthly}, {"disparate", Monthly},
	}
	for _, tt := range tests {
		if got := ParseInterval(tt.in); got != tt.want {
			t.Errorf("ParseInterval(%q) = %q, queria %q", tt.in, got, tt.want)
		}
	}
}

func TestPeriodEndAnchorsOnPreviousEnd(t *testing.T) {
	end := date(2025, time.June, 30)
	// Pagou três dias antes: a data de renovação não muda.
	early := PeriodEnd(date(2025, time.June, 27), &end, Monthly, false)
	if want := date(2025, time.July, 30); !early.Equal(want) {
		t.Errorf("pagamento antecipado deu %s, queria %s", early.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	// Pagou dois dias depois, dentro da tolerância: também não muda.
	late := PeriodEnd(date(2025, time.July, 2), &end, Monthly, false)
	if want := date(2025, time.July, 30); !late.Equal(want) {
		t.Errorf("pagamento em atraso deu %s, queria %s", late.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestPeriodEndReactivationAnchorsOnPayment(t *testing.T) {
	// Esteve cancelada três meses: o novo ciclo conta do pagamento, porque não
	// se cobra tempo que o cliente não teve.
	end := date(2025, time.March, 31)
	got := PeriodEnd(date(2025, time.July, 10), &end, Monthly, true)
	if want := date(2025, time.August, 10); !got.Equal(want) {
		t.Errorf("reactivação deu %s, queria %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestPeriodEndNeverBornExpired(t *testing.T) {
	// Um fim de período muito antigo não pode produzir um ciclo já expirado.
	old := date(2020, time.January, 1)
	now := date(2025, time.July, 10)
	got := PeriodEnd(now, &old, Monthly, false)
	if !got.After(now) {
		t.Errorf("o novo ciclo nasceu expirado: %s", got.Format("2006-01-02"))
	}
}

func TestInstalmentAmount(t *testing.T) {
	total := money.FromMajor(120000, money.AOA) // contrato anual

	// Pago de uma vez: cobra-se o contrato inteiro, sem passar pela divisão.
	if got := InstalmentAmount(total, 0, 12, 12); got.Minor != total.Minor {
		t.Errorf("pagamento único = %d, queria %d", got.Minor, total.Minor)
	}

	// Cobrado ao mês: cada prestação é um doze avos, e não o contrato todo.
	// Era este o erro que cobrava o preço do ano doze vezes.
	monthly := InstalmentAmount(total, 0, 1, 12)
	if want := int64(1000000); monthly.Minor != want {
		t.Errorf("prestação mensal = %d, queria %d", monthly.Minor, want)
	}

	// De cinco em cinco meses num contrato de doze: 5 + 5 + 2.
	if got := InstalmentAmount(total, 0, 5, 12); got.Minor != 5000000 {
		t.Errorf("primeira prestação = %d, queria 5000000", got.Minor)
	}
	if got := InstalmentAmount(total, 2, 5, 12); got.Minor != 2000000 {
		t.Errorf("última prestação = %d, queria 2000000", got.Minor)
	}
}

func TestInstalmentCountRoundsUp(t *testing.T) {
	if got := InstalmentCount(5, 12); got != 3 {
		t.Errorf("InstalmentCount(5, 12) = %d, queria 3", got)
	}
	if got := InstalmentCount(1, 12); got != 12 {
		t.Errorf("InstalmentCount(1, 12) = %d, queria 12", got)
	}
	if got := InstalmentCount(12, 12); got != 1 {
		t.Errorf("InstalmentCount(12, 12) = %d, queria 1", got)
	}
}

func TestInstalmentPlanSumsToTotal(t *testing.T) {
	// Um contrato que não divide certo pelo período não pode perder unidades:
	// a soma das prestações tem de ser exactamente o contrato.
	total := money.New(100003, money.AOA)
	for _, period := range []int{1, 3, 5, 7} {
		plan := InstalmentPlan(total, period, 12)
		var sum int64
		for _, p := range plan {
			sum += p.Minor
		}
		if sum != total.Minor {
			t.Errorf("período %d: soma das prestações = %d, queria %d", period, sum, total.Minor)
		}
	}
}

func TestNormalizeBillingPeriod(t *testing.T) {
	// Zero lê-se como "paga-se de uma vez", ou seja o contrato inteiro.
	if got := NormalizeBillingPeriod(0, 12); got != 12 {
		t.Errorf("período 0 = %d, queria 12", got)
	}
	// Não se cobra em períodos maiores do que o contrato.
	if got := NormalizeBillingPeriod(24, 12); got != 12 {
		t.Errorf("período 24 num contrato de 12 = %d, queria 12", got)
	}
	if got := NormalizeBillingPeriod(1, 12); got != 1 {
		t.Errorf("período 1 = %d, queria 1", got)
	}
}

func TestRenewalWindow(t *testing.T) {
	end := date(2025, time.July, 31)
	w := WindowConfig{LeadDays: 10, GraceDays: 5}.For(end)
	if want := date(2025, time.July, 22); !w.Opens.Equal(want) {
		t.Errorf("abertura = %s, queria %s", w.Opens.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	if got, want := w.Closes.Format("2006-01-02 15:04:05"), "2025-08-05 23:59:59"; got != want {
		t.Errorf("fecho = %s, queria %s", got, want)
	}
	// A tolerância vale até ao fim do dia: cortar à meia-noite tira ao cliente
	// o dia inteiro que lhe foi prometido.
	if !w.Contains(date(2025, time.August, 5)) {
		t.Error("o último dia da tolerância devia estar dentro da janela")
	}
	if w.Contains(date(2025, time.August, 6)) {
		t.Error("o dia a seguir à tolerância não devia estar dentro da janela")
	}
}

func TestProrate(t *testing.T) {
	start := date(2025, time.January, 1)
	end := date(2026, time.January, 1)
	amount := money.FromMajor(36500, money.AOA)

	// Ciclo inteiro pela frente: cobra tudo.
	if got := Prorate(amount, start, start, end); got.Minor != amount.Minor {
		t.Errorf("ciclo inteiro = %d, queria %d", got.Minor, amount.Minor)
	}
	// Ciclo já terminado: não cobra nada, e sobretudo não cobra um valor
	// negativo.
	if got := Prorate(amount, end.AddDate(0, 0, 1), start, end); !got.IsZero() {
		t.Errorf("ciclo terminado = %d, queria 0", got.Minor)
	}
	// A meio do ciclo: cerca de metade.
	half := Prorate(amount, date(2025, time.July, 2), start, end)
	if half.Minor < amount.Minor*45/100 || half.Minor > amount.Minor*55/100 {
		t.Errorf("meio ciclo = %d, queria à volta de %d", half.Minor, amount.Minor/2)
	}
}

func TestUpgradeCreditIgnoresDowngrade(t *testing.T) {
	start := date(2025, time.January, 1)
	end := date(2026, time.January, 1)
	now := date(2025, time.July, 1)
	old := money.FromMajor(10000, money.AOA)
	lower := money.FromMajor(5000, money.AOA)

	// Sem via de reembolso, uma descida de preço não gera crédito: o valor mais
	// baixo entra na renovação seguinte.
	if got := UpgradeCredit(old, lower, now, start, end, false); !got.IsZero() {
		t.Errorf("descida de preço = %d, queria 0", got.Minor)
	}
	// Com crédito permitido, gera um valor negativo proporcional.
	if got := UpgradeCredit(old, lower, now, start, end, true); !got.IsNegative() {
		t.Errorf("descida com crédito = %d, queria negativo", got.Minor)
	}
	// Uma subida cobra a diferença proporcional aos dias que faltam.
	higher := money.FromMajor(20000, money.AOA)
	up := UpgradeCredit(old, higher, now, start, end, false)
	if !up.IsPositive() || up.Minor >= money.FromMajor(10000, money.AOA).Minor {
		t.Errorf("subida de preço = %d, queria positivo e abaixo da diferença inteira", up.Minor)
	}
}

// --- cobertura dos casos de fronteira ------------------------------------------

func TestIntervalMonthsAndStripeMapping(t *testing.T) {
	tests := []struct {
		in     Interval
		months int
		unit   string
		count  int
	}{
		{Monthly, 1, "month", 1},
		{Yearly, 12, "year", 1},
		{Quarterly, 3, "month", 3},
		{Semiannual, 6, "month", 6},
		{Weekly, 0, "week", 1}, // o semanal não se mede em meses
		{Interval("disparate"), 1, "month", 1},
	}
	for _, tt := range tests {
		if got := tt.in.Months(); got != tt.months {
			t.Errorf("%s: meses = %d, queria %d", tt.in, got, tt.months)
		}
		unit, count := tt.in.StripeInterval()
		if unit != tt.unit || count != tt.count {
			t.Errorf("%s: Stripe = %s x%d, queria %s x%d", tt.in, unit, count, tt.unit, tt.count)
		}
		if tt.in.String() != string(tt.in) {
			t.Errorf("String() = %q", tt.in.String())
		}
	}
}

func TestWeeklyAddsSevenDays(t *testing.T) {
	start := date(2025, time.March, 31)
	if got, want := Weekly.Add(start), date(2025, time.April, 7); !got.Equal(want) {
		t.Errorf("semanal = %s, queria %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	// A âncora não se aplica ao semanal: sete dias são sete dias.
	if got, want := Weekly.AddAnchored(start, 1), date(2025, time.April, 7); !got.Equal(want) {
		t.Errorf("semanal com âncora = %s, queria %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestAnchorHelpers(t *testing.T) {
	d := date(2025, time.January, 31)
	if got := AnchorDay(d); got != 31 {
		t.Errorf("dia-âncora = %d, queria 31", got)
	}
	// Âncora a zero cai no dia da própria data.
	if got, want := AddMonthsAnchored(d, 1, 0), date(2025, time.February, 28); !got.Equal(want) {
		t.Errorf("âncora a zero = %s, queria %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	// Ciclo de zero meses lê-se como um.
	if got, want := NthPeriodEnd(d, 0, 2), date(2025, time.March, 31); !got.Equal(want) {
		t.Errorf("ciclo de zero meses = %s, queria %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestStartOfDay(t *testing.T) {
	got := StartOfDay(date(2025, time.July, 15))
	if got.Hour() != 0 || got.Minute() != 0 || got.Second() != 0 {
		t.Errorf("início do dia = %s", got)
	}
	if got.Day() != 15 {
		t.Errorf("dia = %d, queria 15", got.Day())
	}
}

func TestNextCycleEndWrappers(t *testing.T) {
	prev := date(2025, time.January, 31)
	// Sem âncora explícita, usa o dia da data anterior.
	if got, want := NextCycleEnd(prev, 1, 12), date(2025, time.February, 28); !got.Equal(want) {
		t.Errorf("sem âncora = %s, queria %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	// Com âncora, o dia 31 é reposto onde o mês o tem.
	if got, want := NextCycleEndAnchored(date(2025, time.February, 28), 1, 12, 31), date(2025, time.March, 31); !got.Equal(want) {
		t.Errorf("com âncora = %s, queria %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
}

func TestNormalizeContractDurationFallsBackToAYear(t *testing.T) {
	for _, in := range []int{0, -1, -12} {
		if got := NormalizeContractDuration(in); got != 12 {
			t.Errorf("duração %d = %d, queria 12", in, got)
		}
	}
}

func TestInstalmentEdges(t *testing.T) {
	// Uma prestação além do fim do contrato não cobre meses nenhuns.
	if got := InstalmentMonths(5, 5, 12); got != 0 {
		t.Errorf("prestação fora do contrato = %d meses, queria 0", got)
	}
	if got := InstalmentAmount(money.FromMajor(120000, money.AOA), 5, 5, 12); !got.IsZero() {
		t.Errorf("prestação fora do contrato = %s, queria zero", got)
	}
	// Pagamento único devolve uma parcela só, com o total.
	total := money.FromMajor(120000, money.AOA)
	plan := InstalmentPlan(total, 12, 12)
	if len(plan) != 1 || plan[0].Minor != total.Minor {
		t.Errorf("pagamento único = %v", plan)
	}
}

func TestBillingFrequency(t *testing.T) {
	if got := BillingFrequency(1, 12); got != FrequencyMonthly {
		t.Errorf("cobrado ao mês = %q, queria %q", got, FrequencyMonthly)
	}
	if got := BillingFrequency(12, 12); got != FrequencyFixed {
		t.Errorf("pago de uma vez = %q, queria %q", got, FrequencyFixed)
	}
	if got := BillingFrequency(0, 1); got != FrequencyMonthly {
		t.Errorf("contrato de um mês = %q, queria %q", got, FrequencyMonthly)
	}
}

func TestProrateRejectsNonPositive(t *testing.T) {
	start, end := date(2025, time.January, 1), date(2026, time.January, 1)
	zero := money.Zero(money.AOA)
	if got := Prorate(zero, start, start, end); !got.IsZero() {
		t.Errorf("zero prorrateado = %s", got)
	}
	neg := money.New(-1000, money.AOA)
	if got := Prorate(neg, start, start, end); !got.IsZero() {
		t.Errorf("negativo prorrateado = %s, queria zero", got)
	}
}

func TestProrateDays(t *testing.T) {
	start := date(2025, time.January, 1)
	end := date(2025, time.January, 11) // dez dias
	amount := money.FromMajor(1000, money.AOA)

	if got := ProrateDays(amount, start, start, end); got.Minor != amount.Minor {
		t.Errorf("ciclo inteiro = %s, queria %s", got, amount)
	}
	// Cinco dias por cumprir de dez: metade.
	got := ProrateDays(amount, date(2025, time.January, 6), start, end)
	if want := money.FromMajor(500, money.AOA); got.Minor != want.Minor {
		t.Errorf("metade do ciclo = %s, queria %s", got, want)
	}
	// Ciclo terminado não cobra nada.
	if got := ProrateDays(amount, end, start, end); !got.IsZero() {
		t.Errorf("ciclo terminado = %s, queria zero", got)
	}
	// Datas trocadas cobram o valor inteiro em vez de um negativo.
	if got := ProrateDays(amount, start, end, start); got.Minor != amount.Minor {
		t.Errorf("datas trocadas = %s, queria o valor inteiro", got)
	}
	if got := ProrateDays(money.Zero(money.AOA), start, start, end); !got.IsZero() {
		t.Errorf("zero = %s", got)
	}
}

func TestUpgradeCreditWithMismatchedCurrencies(t *testing.T) {
	// Moedas diferentes não geram acerto nenhum, em vez de um número inventado.
	start, end := date(2025, time.January, 1), date(2026, time.January, 1)
	got := UpgradeCredit(money.FromMajor(100, money.EUR), money.FromMajor(200, money.AOA),
		date(2025, time.July, 1), start, end, false)
	if !got.IsZero() {
		t.Errorf("moedas diferentes = %s, queria zero", got)
	}
}

func TestWindowConfigDefaults(t *testing.T) {
	c := WindowConfig{}.WithDefaults()
	if c.LeadDays != DefaultWindow.LeadDays || c.GraceDays != DefaultWindow.GraceDays {
		t.Errorf("por omissão = %+v, queria %+v", c, DefaultWindow)
	}
	if got := (WindowConfig{}).TotalDays(); got != 15 {
		t.Errorf("dias no total = %d, queria 15", got)
	}
	// Sem tolerância só se diz explicitamente. Zero é "não definido" e recebe o
	// valor por omissão, senão quem escreve só o LeadDays fica sem tolerância
	// nenhuma sem dar por isso.
	if got := (WindowConfig{LeadDays: 3}).WithDefaults(); got.GraceDays != DefaultWindow.GraceDays {
		t.Errorf("tolerância omitida = %d, queria o valor por omissão", got.GraceDays)
	}
	if got := (WindowConfig{LeadDays: 3, GraceDays: NoGrace}).WithDefaults(); got.GraceDays != 0 {
		t.Errorf("NoGrace = %d, queria 0", got.GraceDays)
	}
	if got := (WindowConfig{LeadDays: 3, GraceDays: NoGrace}).For(date(2025, time.July, 31)); !got.Closes.Equal(EndOfDay(date(2025, time.July, 31))) {
		t.Errorf("sem tolerância a janela devia fechar no próprio dia: %s", got.Closes)
	}
	if got := (WindowConfig{LeadDays: 2, GraceDays: 4}).TotalDays(); got != 6 {
		t.Errorf("dias no total = %d, queria 6", got)
	}
}

func TestWindowCutoffAndExpired(t *testing.T) {
	now := date(2025, time.July, 15)
	// A janela abre lead-1 dias antes, por isso o corte olha para a frente.
	if got, want := (WindowConfig{LeadDays: 10, GraceDays: 5}).Cutoff(now), date(2025, time.July, 24); !got.Equal(want) {
		t.Errorf("corte = %s, queria %s", got.Format("2006-01-02"), want.Format("2006-01-02"))
	}
	w := WindowConfig{LeadDays: 10, GraceDays: 5}.For(date(2025, time.July, 31))
	if w.Expired(date(2025, time.August, 5)) {
		t.Error("o último dia da tolerância ainda não expirou")
	}
	if !w.Expired(date(2025, time.August, 6)) {
		t.Error("o dia a seguir à tolerância já expirou")
	}
}
