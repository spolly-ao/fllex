# fllex

Biblioteca Go de pagamentos, subscrições e cobranças. Ver `README.md` para a
visão geral.

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

Antes de entregar:

```
gofmt -w . && go build ./... && go vet ./... && go test -cover ./...
```

## Armadilhas conhecidas

- **Deriva de datas.** Usar `cycle.AddMonths` ciclo após ciclo prende quem
  assinou a 31 no dia 28. Para subscrições, usar sempre as variantes com
  dia-âncora (`AddMonthsAnchored`, `NthPeriodEnd`, `PeriodEndAnchored`).
- **Referências de gateway não são intermutáveis.** No MoMenu, o webhook
  correlaciona pelo `transactionId` e a consulta de estado usa o `operationId`.
  Trocá-los devolve sempre "não encontrado". Daí `ProviderRef` e `StatusRef`
  separados.
- **Idempotência do lado de fora.** Nada nesta biblioteca repete
  automaticamente uma cobrança: os gateways angolanos não aceitam chave de
  idempotência, e repetir é cobrar duas vezes. Só leituras são repetidas
  (`httpx.Request.Idempotent`).
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
