<img src="logo.svg" alt="fllex" height="44">

Biblioteca Go de pagamentos, subscrições e cobranças.

Cobre o percurso inteiro de uma cobrança: escolher o gateway, cobrar, confirmar,
renovar, aplicar limites de plano, emitir a factura em PDF e publicar o evento.
Sem dependências externas: só a biblioteca padrão.

```
go get github.com/spolly-ao/fllex
```

## O que traz

**Gateways**, todos por trás do mesmo contrato:

| Pacote | Métodos | Particularidade |
|---|---|---|
| `providers/stripe` | cartão, subscrições recorrentes, Pix em reais | API REST directa, sem SDK; webhooks assinados; o Pix confirma-se no aviso `checkout.session.async_payment_succeeded` |
| `providers/emis` | Multicaixa Express | directo na EMIS, sem agregador; frame de pagamento e callback sem assinatura |
| `providers/baipaga` | BAI PAGA | directo no banco; autorização por OTP no telemóvel, callback assinado e consulta de estado |
| `providers/momenu` | Multicaixa Express, referência | kwanza; emite factura fiscal; webhook assinado e chave de idempotência |
| `providers/proxypay` | referência ATM | fila de pagamentos confirmados como rede de segurança |
| `providers/proxypaydds` | débito directo em conta | mandatos CAP e SAP, fluxo de eventos ordenado |
| `providers/offline` | transferência, atribuição manual | liquidado fora do sistema, confirmado por um operador |
| `wallet` | saldo pré-pago | também é um `payment.Provider` |

**Dinheiro e tempo**, que é onde os erros custam caro:

- `money`: unidades menores inteiras, repartição sem perder cêntimos, conversão de moeda com validade.
- `cycle`: datas de ciclo com dia-âncora, prestações, prorrateamento, janela de renovação.
- `pricing`: escalões por volume ou escalonados, e preço de grupos com titular e membros.

**O ciclo de cobrança**:

- `payment`: modelo canónico, contrato de gateway, registo e resolução por método e moeda.
- `subscription`: motor de renovação: avisos, emissão de cobrança, tolerância, retentativas, expiração.
- `coupon`: cupões de desconto, com as recusas explicadas ao cliente.
- `entitlement`: limites de plano e suspensão reversível dos recursos que passam do tecto.
- `invoice`: proformas, facturas, notas de crédito e recibos de pagamento, com numeração sequencial.
- `invoicepdf`: o PDF desses documentos, escrito à mão sobre a biblioteca padrão, com o logótipo em PNG ou JPEG.
- `mandate`: autorizações de débito directo.
- `tokens`: links de pagamento de uso único.
- `outbox`: eventos que saem se e só se a alteração ficou gravada.
- `worker`: processos periódicos: confirmação por consulta, reconciliação, expiração.

## Como se usa

A biblioteca não impõe tabelas nem migrações. Define interfaces de
armazenamento (`payment.Store`, `subscription.Store`, `invoice.Store`, ...) que
cada projecto implementa sobre o seu Postgres, MySQL ou o que tiver.

### Registar gateways

A ordem de registo é a ordem de preferência: perante um pedido que mais do que
um gateway consegue satisfazer, ganha o primeiro. É assim que se diz "o kwanza
vai pelo gateway local, o resto pelo Stripe" sem escrever um único `if`.

```go
registry := payment.NewRegistry().Register(
    momenu.New(momenu.Config{APIKey: os.Getenv("MOMENU_API_KEY")}),
    stripe.New(stripe.Config{
        SecretKey:     os.Getenv("STRIPE_SECRET_KEY"),
        WebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
    }),
    offline.New(),
)

// O que oferecer na página de pagamento, nesta moeda:
metodos := registry.MethodsFor(money.AOA)
```

Um gateway sem chave configurada continua registado, mas não é escolhido: quem
está abaixo dele apanha o pedido, em vez de a compra falhar.

### Cobrar

```go
res, gateway, err := registry.Charge(ctx, payment.ChargeRequest{
    Reference:   "encomenda-42",
    Amount:      money.FromMajor(5900, money.AOA),
    Method:      payment.MethodMCX,
    Description: "Plano Essencial, mensal",
    Customer:    payment.Customer{Phone: "921234567", Name: "Ana", TaxID: "5417..."},
})

switch res.Kind {
case payment.KindPaid:      // já pago (Multicaixa Express, saldo)
case payment.KindRedirect:  // encaminhar para res.URL
case payment.KindReference: // mostrar res.Entity e res.Reference
case payment.KindCode:      // mostrar res.QRCode
case payment.KindPending:   // aguardar o banco
}
```

### Renovar

```go
motor := subscription.NewEngine(subs, pagamentos, registry, avisos, subscription.Config{
    Window: cycle.WindowConfig{LeadDays: 10, GraceDays: 5},
})
motor.Customers = subscription.CustomerFunc(resolverCliente)
motor.Links = subscription.LinkFunc(gerarLinkDePagamento)

relatorio := motor.Run(ctx) // passagem completa e idempotente
```

O motor é para os métodos que não cobram sozinhos (Multicaixa Express,
referência). No cartão, quem renova é o Stripe: a cobrança com
`Mode: payment.ModeSubscription` abre a subscrição lá, e cada ciclo chega como
`invoice.paid` (ou `invoice.payment_failed`). O aviso não é a última palavra:
a factura confirma-se com `VerifyCharge("in_...")`, e o estado da subscrição
com `payment.SubscriptionVerifier`. Em ensaio, `stripe.Config.TestClock` faz
nascer os clientes num relógio de teste do Stripe, que se adianta pela API para
ver uma renovação sem esperar um mês.

Um primeiro ciclo mais barato (o cupão de um produto que vale só no primeiro)
pede-se com `Amount` igual ao que se cobra agora e `RenewalAmount` igual ao
preço de cada renovação. O Stripe cria a subscrição pelo preço da renovação com
um cupão de `duration: once`, e cobra o primeiro ciclo com o desconto e os
seguintes pelo preço cheio, sozinho. No motor, quem chama cobra o primeiro
ciclo pelo seu valor e renova pelo da subscrição.

Um desconto em mais do que um ciclo acrescenta `DiscountCycles`: N para os
primeiros N ciclos, ou `payment.DiscountForever` para todos, e `RenewalAmount`
passa a ser o preço de tabela. O Stripe recebe um cupão `forever`, ou
`repeating` com os meses que os N ciclos cobrem (o Stripe conta os descontos
repetidos em meses); um desconto de vários ciclos semanais recusa-se com
`payment.ErrUnsupportedDiscount`, antes de se falar com o Stripe.
`DiscountName` é o nome que o cliente vê na factura.

A consulta de uma sessão paga do Stripe devolve também o payment intent por
trás dela (`ChargeStatus.ChargeRef`) e a avaliação do Radar
(`ChargeStatus.RiskLevel`: `normal`, `elevated`, `highest`, `not_assessed`).
O nível é um sinal para quem tem regras de risco próprias, e não muda a
confirmação: uma leitura que falha deixa-o vazio. As contestações do comprador
(`charge.dispute.created`) chegam como `payment.EventDisputeOpened`, com o
payment intent em `ChargeRef`.

### Preços por quantidade

```go
tabela := pricing.Table{
    Mode:     pricing.Graduated,
    Currency: money.AOA,
    Included: 3,
    Base:     money.FromMajor(5000, money.AOA),
    Tiers: []pricing.Tier{
        {UpTo: 10, UnitPrice: money.FromMajor(800, money.AOA)},
        {UnitPrice: money.FromMajor(600, money.AOA)},
    },
}
detalhe, err := tabela.Explain(25) // com a decomposição para mostrar ao cliente
```

### Emitir a factura em PDF

```go
inv, _ := emissor.Invoice(ctx, invoice.Request{ /* ... */ })
pdf, err := invoicepdf.Render(inv, invoicepdf.Options{
    Accent: "#2563EB",
    Logo:   logoPNG,
    Footer: "Empresa, Lda. · NIF 5417000000",
})
```

Quem processa pagamentos por conta de outrem passa o recibo, e não a factura,
que é de quem vende: um documento com `Kind: invoice.KindReceipt` sai como
"Recibo de pagamento", com quem recebeu e quem pagou, o identificador do
pagamento (`PaymentID`) e a frase que diz que não tem valor fiscal.

## Exemplos

`examples/` tem exemplos curtos, um por assunto, com o resultado escrito ao lado
do código. São testes, e por isso não podem ficar desactualizados sem que alguém
dê por isso.

```
go test ./examples/
```

## O que ficou aprendido

A parte que interessa desta biblioteca não são os clientes HTTP: é o
comportamento, e sobretudo os casos que só se descobrem em produção.

**A janela de renovação.** Abre alguns dias antes do fim do ciclo e fecha alguns
dias depois. Durante toda ela a cobertura mantém-se e a referência continua
válida, o que faz da tolerância cobertura a sério e não um adiamento do corte.

**A validade da referência cobre a janela inteira**, e não as 24 horas de uma
cobrança avulsa. Uma referência que morre no dia seguinte deixa o cliente com um
número que não pode pagar durante a tolerância que lhe foi prometida.

**A renovação cobra o preço de tabela**, não o que foi pago. Um cupão vale para
o primeiro ciclo, e só acompanha as renovações quando alguém decidiu isso
explicitamente.

**A prestação não é o contrato.** Um contrato anual cobrado ao mês cobra um doze
avos por mês. Enquanto as duas coisas forem uma só, cobra-se o preço do ano doze
vezes por ano.

**As datas não derivam.** Quem assina a 31 é cobrado a 28 em Fevereiro e volta
ao 31 em Março. Sem dia-âncora, o mês curto trunca a data e ela fica presa no 28
até ao fim do contrato.

**Escalonado e por volume não são a mesma coisa.** O escalonado cobra cada
patamar pelas unidades que lhe cabem; o por volume cobra tudo ao preço do
patamar onde a quantidade cai, e cria um degrau em que comprar mais fica mais
barato.

**Descer de plano não pode apagar dados.** Os recursos acima do limite ficam
suspensos e voltam sozinhos se o plano subir. Suspender por ordem de criação é a
única regra que dá o mesmo resultado duas vezes seguidas e que se explica ao
cliente numa frase.

**O Multicaixa Express não tem webhook.** A resposta HTTP é o único sinal de que
o pagamento passou, e quando ela se perde o dinheiro foi cobrado e o nosso lado
não sabe. `momenu.Reconciler` recupera esses pagamentos correlacionando as
facturas do gateway por telemóvel, valor e janela de tempo.

**O relatório do gateway é o que se compara com o livro.** Os gateways com
relatório na API implementam `payment.StatementReader` (o Stripe, pelas linhas
do saldo; o MoMenu, pelas facturas, só em contas Pro); os que só o deixam
descarregar do painel implementam `payment.StatementImporter`, que lê o CSV pelo
nome das colunas (o Proxypay e o BAI PAGA). Uma leitura incompleta é um erro, e
nunca uma lista parcial: uma linha em falta faz um pagamento verdadeiro parecer
inventado.

**A fila do Proxypay é a rede de segurança.** Aplica-se o efeito primeiro e só
depois se retira o pagamento da fila. Pela ordem contrária, um processo que
morra no meio deixa o pagamento fora da fila e sem efeito nenhum.

**Um mandato por activar não é uma falha.** É o titular que ainda não foi ao
banco, e não deve gastar tentativas de retentativa nem disparar alarmes.

**O evento sai se e só se a alteração ficou gravada.** O `outbox` escreve a
mensagem na mesma transacção; um processo à parte entrega-a. O preço é a entrega
poder repetir-se, e por isso o consumidor tem de ser idempotente.

**Um PDF de factura escreve-se em WinAnsi.** Escrever UTF-8 num fluxo que o
leitor interpreta como WinAnsi transforma "Subscrição" em lixo, e é o defeito
que torna um gerador inútil num mercado de língua portuguesa.

## Estado

Compila e passa os testes com Go 1.25+, com **100% de cobertura de instruções
em todos os pacotes**. O `examples/` não conta: não tem código fora dos testes,
porque os exemplos são eles próprios os testes.

Os 100% não são um número para exibir: foram a ferramenta de encontrar defeitos.
Perseguir cada linha por correr trouxe à superfície uma janela de renovação sem
tolerância nenhuma para quem só configurasse a antecedência, uma confirmação que
travava todas as cobranças por referência, um valor de menos um cêntimo escrito
como `-0` na factura, e meia dúzia de defesas que nunca chegavam a correr e que
por isso não defendiam nada. Cada uma dessas está hoje coberta por um teste que
diz o que se partia.

```
gofmt -w . && go build ./... && go vet ./... && go test ./...
```

## Licença

MIT, como os SDK de JavaScript e de PHP. Está em `LICENSE`, e não é só
formalidade: sem um ficheiro de licença que o detector reconheça, o
[pkg.go.dev](https://pkg.go.dev/github.com/spolly-ao/fllex) marca o módulo como
não redistribuível e **não mostra a documentação**. Quem instala um SDK de
pagamentos tem o direito de ler o que está a pôr no servidor dele, e isso inclui
poder lê-lo onde o procura.
