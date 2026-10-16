# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

# fllex

Biblioteca Go de pagamentos, subscrições e cobranças. Ver `README.md` para a
visão geral.

É o submódulo `shared/lib` do espaço de trabalho `fllex-workspace` e um dos três
repositórios **públicos** da casa: tudo o que aqui entra é lido por quem
integra. O `services/core` importa-a como `github.com/spolly-ao/fllex`, e em
local usa esta pasta pelo `go.work` da raiz do espaço de trabalho, nunca por um
`replace` no `go.mod`. Uma alteração de comportamento que o core precise de
acompanhar faz-se nos dois na mesma passagem, e commita-se a partir da raiz com
`bash bin/fllex commit -m "..."`.

## Comandos

```
gofmt -w . && go build ./... && go vet ./... && go test -cover ./...

go test ./... -race -count=1 -timeout=5m   # o que a CI corre
go test ./subscription/ -run TestNome -v   # um teste só
go test -coverprofile=c.out ./cycle/ && go tool cover -func=c.out | grep -v 100.0%
go test ./examples/                        # os exemplos, que são testes Example
```

A CI (`.github/workflows/ci.yml`) falha com `gofmt -l` não vazio, e corre os
testes com `-race`: o motor de renovação corre dentro de servidores HTTP com
pedidos em paralelo.

## Arquitectura

**`payment` é o centro.** Define o modelo canónico (`Payment`, `Status`,
`Method`, `Event`), o contrato `Provider` e o `Registry`. A ordem de registo é a
ordem de preferência, e um provider sem credenciais fica registado mas não é
escolhido: é assim que o kwanza vai pelo gateway local e o resto pelo Stripe
sem um `if`. As capacidades opcionais (`Verifier`, `Canceller`, `ExpiryHonorer`, `Subscriber`, `SubscriptionVerifier`,
`WebhookParser`, `Refunder`, `Reporter`) estão em `payment/provider.go`, e os
relatórios para a reconciliação (`StatementReader` pela API, `StatementImporter`
pelo CSV do painel do gateway) em `payment/statement.go`.

**`providers/*` traduzem, e só eles.** Cada um converte `ChargeRequest` para a
língua do gateway e as respostas e webhooks de volta para `payment.Event`,
sobre o transporte comum de `internal/httpx` (erros do gateway, repetição só de
leituras). `providers/ensaio` não fala com rede nenhuma: é o que as contas em
modo de teste usam, e não pode ter caminho para produção. `wallet` também é um
`payment.Provider`.

**Por cima do `payment`, os pacotes de ciclo de vida** usam só interfaces:
`subscription.Engine` junta `subscription.Store`, `payment.Store`, o `Registry`
e um `Notifier`, e decide avisos, emissão, tolerância e expiração a partir das
janelas de `cycle`; `worker` corre as passagens periódicas (consulta de estado,
reconciliação, expiração); `outbox` guarda o evento na transacção do facto e
entrega-o depois. `money`, `cycle`, `pricing` e `phone` são folhas puras sem
estado.

**`examples/` são testes `Example` com `// Output:`**, um ficheiro por assunto.
Um comportamento público que mude parte o exemplo correspondente, e é para
corrigir lá também.

## Regras do projecto

**Zero dependências externas.** Só a biblioteca padrão. É uma restrição de
desenho e não uma coincidência: a biblioteca é importada por serviços com
versões diferentes de tudo, e cada dependência que traga é uma actualização em
cadeia que lhes impõe. Um SDK de gateway substitui-se por chamadas HTTP
directas; um gerador de PDF substitui-se por escrever o formato à mão, que é o
que o pacote `invoicepdf` faz.

**A biblioteca não conhece bases de dados.** Nenhum pacote importa um ORM, gera
SQL ou impõe um esquema. O que define são interfaces de armazenamento
(`payment.Store`, `subscription.Store`, `coupon.Store`, ...) que cada projecto
implementa sobre o que já tem. Se uma funcionalidade parecer precisar de uma
tabela, define-se uma interface.

**Identificadores são `string`.** Há sistemas a usar UUID, texto e inteiro
autoincremental. Impor um tipo era impedir metade deles de adoptar a biblioteca.

**Dinheiro é `money.Amount`**, nunca `float64` nem inteiros soltos. A conversão
para o que cada gateway espera (kwanzas inteiros no MoMenu, decimal em string no
Proxypay, unidades menores no Stripe) é feita dentro do respectivo pacote de
provider e em mais lado nenhum.

**Capacidades opcionais por asserção de tipo.** `payment.Provider` é o mínimo:
nome, métodos, moedas, configuração e cobrar. Tudo o resto (consultar estado,
cancelar, gerir subscrições, ler webhooks, estornar, relatar) são interfaces
separadas que cada provider implementa se souber fazer. Não acrescentar métodos
a `Provider` que metade dos gateways teria de implementar a devolver "não
suportado".

**As recusas trazem mensagem para o cliente.** `coupon.Error` e
`entitlement.LimitError` têm um campo `Message` escrito para ser mostrado. Um
cupão ou um limite recusado sem explicação é uma venda perdida e um pedido de
suporte.

## Estilo

Documentação em português europeu, com acentuação correcta. Sem travessões.

Os comentários explicam **porquê**, não o quê. A regra prática: se o comentário
descreve o que a linha seguinte faz, apaga-se; se descreve o caso de produção
que obrigou a fazer assim, fica. Vários comentários desta biblioteca descrevem
erros reais que custaram dinheiro (cobrar o preço do ano doze vezes, referências
que morrem antes da tolerância acabar, pagamentos do Multicaixa Express
perdidos, acentos comidos num PDF). Esses são os que mais importam manter.

Nomes de tipos e funções em inglês (é uma biblioteca Go); documentação,
comentários e mensagens de erro em português.

## Testes

Concentram-se na lógica que custa dinheiro quando está errada: aritmética de
dinheiro, datas de ciclo, prestações, escalões, transições de estado, resolução
de gateway, motor de renovação, validação de cupões, limites de plano, entrega
de eventos, assinatura de webhooks, reconciliação e codificação do PDF.

Os gateways testam-se com `httptest`, verificando o que é **enviado** e não só o
que é devolvido: o valor em kwanzas inteiros, o telemóvel normalizado, a
validade da referência, os metadados propagados para a subscrição.

O PDF testa-se descomprimindo os fluxos e procurando os bytes esperados. É a
única forma de garantir que "Subscrição" chega ao papel como "Subscrição".

**Todos os pacotes estão a 100% de cobertura, e é para aí que voltam.** A regra
não é decorativa: quando uma linha não consegue ser alcançada por teste nenhum,
quase sempre é porque não pode mesmo ser alcançada em produção, e uma defesa que
nunca corre não defende nada. Nesses casos apaga-se a linha e fica um comentário
a dizer porque é que o caso não existe, em vez de se escrever um teste artificial
para lá chegar.

Antes de entregar, a primeira linha da secção "Comandos", com todos os pacotes a
100%.

## Armadilhas conhecidas

- **Deriva de datas.** Usar `cycle.AddMonths` ciclo após ciclo prende quem
  assinou a 31 no dia 28. Para subscrições, usar sempre as variantes com
  dia-âncora (`AddMonthsAnchored`, `NthPeriodEnd`, `PeriodEndAnchored`).
- **Referências de gateway não são intermutáveis, e a do MoMenu inverteu-se.**
  O `ProviderRef` e o `StatusRef` são separados porque há gateways que
  correlacionam o webhook por um identificador e aceitam a consulta de estado por
  outro. Esta nota dizia que no MoMenu a consulta era pelo `operationId`; hoje é
  pelo `transactionId`, e enquanto o `StatusRef` levou o outro a consulta
  devolvia "não encontrado" e **nenhuma referência paga chegava a fechar**. Ao
  mudar de gateway ou de versão de API, confirmar qual é qual antes de confiar
  no que está escrito aqui.
- **Idempotência do lado de fora, e o MoMenu é a excepção.** Nada nesta
  biblioteca repete automaticamente uma cobrança: o `httpx` repete o pedido tal
  como ele é, e um gateway sem chave de idempotência cobra duas vezes. Só
  leituras são repetidas (`httpx.Request.Idempotent`).

  **O MoMenu passou a aceitar `Idempotency-Key`** nas cobranças, válida 24 horas
  e só para respostas de sucesso: ali, repetir com a mesma chave devolve a
  resposta original e não cobra outra vez. O `providers/momenu` manda a nossa
  referência como chave, e é isso que torna recuperável uma resposta de
  Multicaixa Express perdida a meio de três minutos. A regra geral continua a
  valer para os outros, e esta nota existe porque a armadilha estava escrita aqui
  como se valesse para todos.

- **A documentação de um gateway muda sem avisar, e a nossa não.** O MoMenu
  mudou o caminho da consulta de estado, o identificador que ela recebe, a forma
  do corpo, passou a assinar os webhooks e retirou o eKwanza, e nada disto deu
  erro de compilação: a integração ficou a devolver "pendente" para sempre. Ao
  tocar num provider, reler a documentação dele primeiro, e desconfiar
  especialmente dos comentários que descrevem o que o gateway faz.
- **Ordem nas filas.** Aplicar o efeito antes de retirar o pagamento da fila do
  Proxypay. Ao contrário, um processo que morra no meio perde o pagamento para
  sempre. O mesmo vale para o `outbox`: entregar, depois marcar.
- **Limites de recursos desconhecidos são zero, não infinito.**
  `entitlement.Limits` devolve zero para um recurso que não esteja no mapa. Um
  limite esquecido na configuração de um plano novo bloqueia a criação, que se
  nota logo, em vez de abrir a porta, que só se nota na factura.
- **Suspenso não é desligado.** `entitlement.Resource` tem os dois campos
  separados de propósito: quem sobe de plano quer os suspensos de volta, mas
  não quer ressuscitar o que um administrador desligou.
- **O `fill-rule` do SVG e o do PDF não são o mesmo problema, mas rimam.** No
  PDF, as sobreposições de caminhos com a mesma orientação unem-se; com
  orientações opostas, furam. É o que faz o olho do `e` ser um buraco e a barra
  do `f` ser tinta.
