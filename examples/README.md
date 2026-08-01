# Exemplos

Exemplos curtos, um por assunto. Cada um trata de uma coisa só e traz o
resultado escrito ao lado do código, no comentário `// Output:`, para se
perceber sem correr nada.

```
go test ./examples/
```

São testes a sério: o `go test` corre-os e compara a saída. Um exemplo que
deixe de estar certo falha, em vez de enganar quem o lê.

| Ficheiro | O que mostra |
|---|---|
| `cobrar_test.go` | Registar gateways e cobrar. O `Kind` da resposta diz o que mostrar a seguir. Um gateway sem chave fica registado mas não é escolhido. |
| `subscricoes_test.go` | Renovar sem deriva de datas, a renovação cobrar o preço de tabela e não o que foi pago, e a tolerância a dar cobertura a sério. |
| `ciclos_test.go` | A janela de renovação, o dia-âncora de quem assina a 31, as prestações de um contrato anual e o prorrateamento. |
| `dinheiro_test.go` | Unidades menores inteiras e a repartição que não perde cêntimos. |
| `precos_test.go` | Escalões escalonados e por volume, com a decomposição para mostrar ao cliente. |
| `cupoes_test.go` | Percentagem, valor fixo maior do que a compra, e o desconto que acompanha (ou não) as renovações. |
| `limites_test.go` | Limites de plano e a suspensão reversível de quem desce de plano. |
| `facturas_test.go` | Proforma, factura e o PDF. Funcionam com qualquer método de pagamento. |
| `webhooks_test.go` | O mesmo evento normalizado a sair de gateways que falam línguas diferentes. |

## O que não está aqui

Chamadas a gateways a sério, que precisariam de credenciais e de rede. O que se
mostra é o que se passa antes e depois: o pedido que se monta, e o evento ou o
resultado que se trata. A ligação em si é uma chamada HTTP que a biblioteca faz
por si.

Os armazenamentos em memória que aparecem no fim de dois ficheiros são o mínimo
para o exemplo correr. Não são para copiar: o que interessa é o contrato que
implementam.
