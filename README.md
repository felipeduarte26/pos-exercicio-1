# Cliente-Servidor — Cotação USD/BRL

Aplicação em Go que implementa um fluxo cliente-servidor para consultar a cotação do dólar (USD-BRL), persistir o histórico em SQLite e gravar o valor atual em arquivo de texto. O projeto foi pensado como exercício prático de APIs HTTP, contextos com timeout, cancelamento de requisições e persistência local.

## Visão geral

| Componente | Função |
|------------|--------|
| **Servidor** (`server/server.go`) | Expõe `GET /cotacao` na porta `8080`, consulta a [AwesomeAPI](https://economia.awesomeapi.com.br/), grava cada cotação no banco e devolve o `bid` em JSON. |
| **Cliente** (`client/client.go`) | Chama o servidor, respeita um timeout de 300 ms e grava o resultado em `cotacao.txt` no formato `Dólar: {valor}`. |

## Pré-requisitos

- [Go](https://go.dev/dl/) **1.27.3** ou superior
- Compilador C (necessário para o driver `github.com/mattn/go-sqlite3`)
- Acesso à internet (consulta à AwesomeAPI)

## Como executar

Em um terminal, inicie o servidor:

```bash
go run ./server
```

O servidor ficará disponível em `http://localhost:8080/cotacao`.

Em outro terminal, execute o cliente:

```bash
go run ./client
```

Se tudo correr bem, o cliente cria ou atualiza o arquivo `cotacao.txt` na raiz do projeto com uma linha no formato:

```text
Dólar: 5.1234
```

## API do servidor

**Endpoint:** `GET /cotacao`

**Resposta de sucesso (200):**

```json
{
  "bid": "5.1234"
}
```

**Possíveis erros:**

| Status | Situação |
|--------|----------|
| `502` | Falha ao consultar a AwesomeAPI |
| `504` | A API de câmbio não respondeu dentro do limite de **200 ms** |

Falhas ao gravar no SQLite são apenas registradas em log; o cliente ainda recebe a cotação obtida da API, desde que a consulta externa tenha sido bem-sucedida.

## Timeouts e comportamento

Os limites de tempo fazem parte do desenho do exercício:

| Etapa | Timeout |
|-------|---------|
| Consulta à AwesomeAPI (servidor) | 200 ms |
| Gravação no SQLite (servidor) | 10 ms |
| Requisição ao servidor (cliente) | 300 ms |

O servidor usa o contexto da requisição HTTP para cancelar a chamada à API se o cliente desistir. Após obter a cotação, a gravação no banco usa `context.WithoutCancel` para tentar persistir mesmo que o cliente tenha encerrado a conexão, ainda dentro do limite de 10 ms para o INSERT.

O SQLite é configurado com journal mode **WAL** e `synchronous=NORMAL`, com **uma única conexão** aberta, para reduzir contenção de escrita (`database is locked`) em requisições concorrentes.

## Banco de dados

Na primeira execução, o servidor cria o arquivo `cotacoes.db` (ignorado pelo Git) e a tabela `cotacoes`, registrando os campos retornados pela API e um timestamp de inserção local (`created_at`).

## Estrutura do repositório

```text
.
├── client/
│   └── client.go      # Cliente HTTP
├── server/
│   └── server.go      # Servidor HTTP + SQLite
├── go.mod
├── go.sum
└── README.md
```

Arquivos gerados em tempo de execução (`cotacao.txt`, `cotacoes.db` e arquivos auxiliares do WAL) estão listados no `.gitignore`.

## Dependências

- `github.com/mattn/go-sqlite3` — driver SQLite para Go

Instalação automática ao executar `go run` ou `go build` na pasta do módulo.

## Encerramento do servidor

O servidor trata `SIGINT` e `SIGTERM` e faz *shutdown* gracioso da API HTTP com timeout de 5 segundos.

## Licença

Consulte o repositório ou o autor do projeto para informações de licenciamento, se aplicável.