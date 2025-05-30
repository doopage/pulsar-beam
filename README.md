[![Join the chat at https://gitter.im/FaradayRF/Lobby](https://badges.gitter.im/Join%20Chat.svg)](https://gitter.im/kafkaesque-io/community?utm_source=badge&utm_medium=badge&utm_content=badge)
[![Go Report Card](https://goreportcard.com/badge/github.com/kafkaesque-io/pulsar-beam)](https://goreportcard.com/report/github.com/kafkaesque-io/pulsar-beam)
[![CI Build](https://github.com/kafkaesque-io/pulsar-beam/workflows/ci/badge.svg
)](https://github.com/kafkaesque-io/pulsar-beam/actions)
[![Language](https://img.shields.io/badge/Language-Go-blue.svg)](https://golang.org/)
[![codecov](https://codecov.io/gh/kafkaesque-io/pulsar-beam/branch/master/graph/badge.svg)](https://codecov.io/gh/kafkaesque-io/pulsar-beam)
[![Docker image](https://shields.beevelop.com/docker/image/image-size/kafkaesqueio/pulsar-beam/0.22.svg?style=round-square)](https://hub.docker.com/r/kafkaesqueio/pulsar-beam/)
[![LICENSE](https://img.shields.io/hexpm/l/pulsar.svg)](https://github.com/kafkaesque-io/pulsar-beam/blob/master/LICENSE)

# Pulsar Beam

Beam is an http based streaming and queueing system backed up by Apache Pulsar.

- [x] A message can be sent to Pulsar via an HTTP POST method as a producer.
- [x] A message can be pushed to a webhook or Cloud Function for consumption.
- [x] A webhook or Cloud Function receives a message, process it and reply another message, in a response body, back to another Pulsar topic via Pulsar Beam.
- [x] Messages can be streamed via HTTP Sever Sent Event, [SSE](https://www.html5rocks.com/en/tutorials/eventsource/basics/)
- [x] Support HTTP polling of batch messages

Opening an issue and PR are welcomed! Please email `contact@kafkaesque.io` for any inquiry or demo.

## Advantages
1. Since Beam speaks http, it is language and OS independent. You can take advantage of powerhouse of Apache Pulsar without limitation of client library and OS.

Immediately, Pulsar can be supported on Windows and any languages with HTTP support.

2. It has a very small footprint with a 15MB docker image size.

3. Supports HTTP SSE streaming

## Interface

REST API and endpoint swagger document is published at [this link](https://kafkaesque-io.github.io/pulsar-beam-swagger/)

### Endpoint to send messages
This is the endpoint to `POST` a message to Pulsar. 

```
/v2/firehose/{persistent}/{tenant}/{namespace}/{topic}
```
Valid values of {persistent} are `p`, `persistent`, `np`, `nonpersistent`

These HTTP headers may be required to map to Pulsar topic.
1. Authorization -> Bearer token as Pulsar token
2. PulsarUrl -> *optional* a fully qualified pulsar or pulsar+ssl URL where the message should be sent to. It is optional. The message will be sent to Pulsar URL specified under `PulsarBrokerURL` in the pulsar-beam.yml file if it is absent.

### Endpoint to stream HTTP Server Sent Event
This is the endpoint to `GET` messages from Pulsar as a consumer subscription
```
/v2/sse/{persistent}/{tenant}/{namespace}/{topic}
```
Valid values of {persistent} are `p`, `persistent`, `np`, `nonpersistent`

These HTTP headers may be required to map to Pulsar topic.
1. Authorization -> Bearer token as Pulsar token
2. PulsarUrl -> *optional* a fully qualified pulsar or pulsar+ssl URL where the message should be sent to. It is optional. The message will be sent to Pulsar URL specified under `PulsarBrokerURL` in the pulsar-beam.yml file if it is absent.

Query parameters
1. SubscriptionType -> Supported type strings are `exclusive` as default, `shared`, and `failover`
2. SubscriptionInitialPosition -> supported type are `latest` as default and `earliest`
3. SubscriptionName -> the length must be 5 characters or longer. An auto-generated name will be provided in absence. Only the auto-generated subscription will be unsubscribed.

### Endpoint to poll batch messages
Polls a batch of messages always from the earliest subscription position from a topic.
```
/v2/poll/{persistent}/{tenant}/{namespace}/{topic}
```
These HTTP headers may be required to map to Pulsar topic.
1. Authorization -> Bearer token as Pulsar token
2. PulsarUrl -> *optional* a fully qualified pulsar or pulsar+ssl URL where the message should be sent to. It is optional. The message will be sent to Pulsar URL specified under `PulsarBrokerURL` in the pulsar-beam.yml file if it is absent.

Query parameters
1. SubscriptionType -> Supported type strings are `exclusive` as default, `shared`, and `failover`
2. SubscriptionName -> the length must be 5 characters or longer. An auto-generated name will be provided in absence. Only the auto-generated subscription will be unsubscribed.
3. batchSize -> Replies to a client when the batch size limit is reached. The default is 10 messages per batch. 
4. perMessageTimeoutMs -> is a time out to wait for the next message's arrival from a Pulsar topic. It is in milliseconds per message. The default is 300ms.

### Webhook registration
Webhook registration is done via REST API backed by a database of your choice, such as MongoDB, in momery cache, and Pulsar itself. Yes, you can use a compacted Pulsar topic as a database table to perform CRUD. The configuration parameter is `"PbDbType": "inmemory",` in the `pulsar_beam.yml` file or the env variable `PbDbType`.

#### Webhook or Cloud function management API
The management REAT API has this endpoint. Here is [the swagger document](https://kafkaesque-io.github.io/pulsar-beam-swagger/#/Create-or-Update-Topic)
```
/v2/topic
```

#### Bearer Token Authentication
Pulsar Beam can decode and authenticate JWT generated by Pulsar. Webhook management requires a subject in JWT that matches the tenant name in the topic full name. `pulsar-admin token` can be used to generate such token.

Pulsar Beam requires the same public and private keys to generate and verify JWT. These public and private key should be specified in the config to be loaded.

To disable JWT authentication, set the paramater `HTTPAuthImpl` in the config file or env variable to `noauth`.

Notice: Pulsar Beam create one client connection per pulsar url per token, so using other authorization on top of Pulsar Beam may cause memory leak due to creating of a lot of pulsar client. In order to use other authorization like reverse proxy (like nginx) on top of Pulsar Beam, please disable Pulsar authorization by setting `PulsarTokenHeaderName` to empty string (default is "Authorization"). If you would like to keep both authorization of reverse proxy and Pulsar, please change `PulsarTokenHeaderName` to another header name that is different than "Authorization" or not using by reverse proxy.

How to know that you are under memory leak?

- Option 1: Use `gops` to check running go routine and if you are having a lot of routine that doing ping/pong with Pulsar brokers.
- Option 2: We are using Pulsar Beam as http webhook receiver at https://doopage.com and we are able to handle few millions of request every day with CPU stable at <3% (8vCPU AWS) and Memory <40MB (`WorkerPoolSize` = 16). If you are using more resources than us, please try to set `PulsarTokenHeaderName` to empty string to check whether the problem is resolved.

### Sink source

If a webhook's response contains a body and three headers including `Authorization` for Pulsar JWT, `TopicFn` for a topic fully qualified name, and `PulsarUrl`, the beam server will send the body as a new message to the Pulsar's topic specified as in TopicFn and PulsarUrl.

### Server configuration

Both [json](./config/pulsar_beam.json) and [yml format](./config/pulsar_beam.yml) are supported as configuration file. The configuration paramters are specified by [config.go](https://github.com/kafkaesque-io/pulsar-beam/blob/master/src/util/config.go#L25). Every parameter can be overridden by an environment variable with the same name.

#### Server Mode
In order to offer high performance and division of responsiblity, webhook and receiver endpoint can run independently `-mode broker` or `-mode receiver`. By default, the server runs in a hybrid mode with all features running in the same process.


### Docker image and Docker builds
The docker image can be pulled from dockerhub.io.
```
$ sudo docker pull kafkaesqueio/pulsar-beam
```

Here are steps to build docker image and run docker container in a file based configuration.

1. Build docker image
```
$ sudo docker build -t pulsar-beam .
```

2. Run docker
This is an example of a default configurations using in-memory database. Customized `pulsar_beam.yml` and private and public key files can be mounted and passed in as an env variable `PULSAR_BEAM_CONFIG`. The certificate is required to connect to Pulsar with TLS enabled.

```
$ sudo docker run -d -it -v /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem:/etc/ssl/certs/ca-bundle.crt -p 8085:8085 --name=pbeam-server pulsar-beam
```

`gops` is built in the docker image for troubleshooting purpose.

### Pulsar Kubernetes cluster deployment

Pulsar Beam can be deployed within the same cluster as Pulsar. This [helm chart](https://github.com/kafkaesque-io/pulsar-helm-chart/blob/master/helm-chart-sources/pulsar/templates/beamwh-deployment.yaml) deploys a webhook broker in its own pod. The rest of HTTP receiver endpoint and REST API are deployed as a container within the [Pulsar proxy pod](https://github.com/kafkaesque-io/pulsar-helm-chart), that offers scalability with multiple replicas.


## Dev set up
Clone the repo at your gopath src/github.com/kafkaesque-io/pulsar-beam folder.

### Linting
Install golint.
```bash
$ go install github.com/golang/lint
```

```bash
$ cd src
$ golint ./...
```

There are two scripts used for CI. You might want to run them in the local environment before submitting a PR.
This [CI script](./scripts/ci.sh) does linting, go vet and go build.
The [code coverage script](./scripts/test_coverage.sh) runs unit test and tallies up the code coverage.

### How to run 
The steps how to start the web server.
```bash
$ cd src
$ go run main.go
```

### Local CI, unit test and end to end test
There are scripts under `./scripts` folder to run code analysis, vetting, compilation, unit test, and code coverage manually as all of these are part of CI checks by Github Actions.

One end to end test is under `./src/e2e/e2etest.go`, that performs the following steps in order:
1. Create a topic and its webhook via RESTful API. The webhook URL can be an HTTP triggered Cloud Function. CI process uses a GCP 
2. Send a message to Pulsar Beam's v1 injestion endpoint
3. Waiting on the sink topic where the first message will be sent to a GCP Cloud Function (in CI) and in turn reply to Pulsar Beam to forward to the second sink topic
4. Verify the replied message on the sink topic
5. Delete the topic and its webhook document via RESTful API

Since the set up is non-trivial involving Pulsar Beam, a Cloud function or webhook, the test tool, and Pulsar itself with SSL, we recommend to take advantage of [the free plan at kesque.com](https://kesque.com) as the Pulsar server and a Cloud Function that we have verified GCP Fcuntion, Azure Function or AWS Lambda will suffice in the e2e flow.

 Step to perform unit test
```bash
$ cd src/unit-test
$ go test -v .
```
