binary       = .build/klara
binary-imapd = .build/imapd
binary-msa   = .build/msa
binary-mta   = .build/mta
binary-smtpd = .build/smtpd

pbdir     = pb
pbdir-int = internal/pb
protodir  = proto
sourcedir = .

sources       := $(shell find $(sourcedir) -type f -name '*.go' -not -path './cmd/*')
sources-imapd := $(shell find ./cmd/imapd/ -type f -name '*.go')
sources-msa   := $(shell find ./cmd/msa/ -type f -name '*.go')
sources-mta   := $(shell find ./cmd/mta/ -type f -name '*.go')
sources-smtpd := $(shell find ./cmd/smtpd/ -type f -name '*.go')

protos     := $(shell find $(protodir) -type f -name '*.proto' -not -path '*/detail/*')
protos-int := $(shell find $(protodir) -type f -name '*.proto' -path '*/detail/*')

githubrepo = $(shell awk 'NR==1 {print $$2; exit}' go.mod)

.SILENT: lint

%:
	@:

all: $(binary) $(binary-imapd) $(binary-msa) $(binary-mta) $(binary-smtpd)

$(binary): $(sources)
	go build -o $(binary) main.go

$(binary-imapd): $(sources-imapd) $(sources)
	go build -o $(binary-imapd) ./cmd/imapd/

$(binary-msa): $(sources-msa) $(sources)
	go build -o $(binary-msa) ./cmd/msa/

$(binary-mta): $(sources-mta) $(sources)
	go build -o $(binary-mta) ./cmd/mta/

$(binary-smtpd): $(sources-smtpd) $(sources)
	go build -o $(binary-smtpd) ./cmd/smtpd/

clean:
	go clean
	if [ -f $(binary) ] ; then rm $(binary); fi
	if [ -f $(binary-mta) ] ; then rm $(binary-mta); fi
	if [ -f $(binary-smtpd) ] ; then rm $(binary-smtpd); fi

lint:
	gofmt -s -d -e $(sourcedir)
	echo '' # Empty line to separate the outputs
ifeq (, $(shell which clang-format))
	echo '\033[1;41m WARN \033[0m clang-format not found, not linting files';
else
	clang-format --style=file --dry-run $(protos) $(protos-int)
endif

lint\:ci:
	test -z "$(shell gofmt -s -l $(sourcedir))"
	clang-format --style=file --dry-run --Werror $(protos) $(protos-int)

lint\:fix:
	gofmt -s -l -w $(sourcedir)
	clang-format --style=file -i $(protos) $(protos-int)

protoc-gen-go:
ifeq (, $(shell which protoc-gen-go))
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
endif

protoc-gen-go-grpc:
ifeq (, $(shell which protoc-gen-go-grpc))
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
endif

protoc-int: $(protos-int) protoc-gen-go protoc-gen-go-grpc
	protoc \
		--proto_path=$(protodir) \
		--go_out=$(pbdir-int) --go_opt=module=$(githubrepo)/$(pbdir-int) \
		--go-grpc_out=$(pbdir-int) --go-grpc_opt=module=$(githubrepo)/$(pbdir-int) \
		$(protos-int)

protoc: protoc-int
	protoc \
		--proto_path=$(protodir) \
		--go_out=$(pbdir) --go_opt=module=$(githubrepo)/$(pbdir) \
		--go-grpc_out=$(pbdir) --go-grpc_opt=module=$(githubrepo)/$(pbdir) \
		$(protos)

run:
	go run -race . \
		-debug \
		-addr localhost:50151 \
		-pubsub-project local \
		-pubsub-topic mail \
		$(filter-out $@,$(MAKECMDGOALS)) $(MAKEFLAGS)

run\:imapd:
	go run -race ./cmd/imapd/ \
		-addr localhost:1143 \
		-debug \
		$(filter-out $@,$(MAKECMDGOALS)) $(MAKEFLAGS)

run\:msa:
	go run -race ./cmd/msa/ \
		-addr localhost:1587 \
		-debug \
		-pubsub-project local \
		-pubsub-topic mail \
		$(filter-out $@,$(MAKECMDGOALS)) $(MAKEFLAGS)

run\:mta:
	go run -race ./cmd/mta/ \
		-addr localhost:50152 \
		-debug \
		-space-id local \
		-tknz-addr localhost:8080 \
		$(filter-out $@,$(MAKECMDGOALS)) $(MAKEFLAGS)

run\:smtpd:
	go run -race ./cmd/smtpd/ \
		-addr localhost:2525 \
		-debug \
		-pubsub-project local \
		-pubsub-topic mail \
		$(filter-out $@,$(MAKECMDGOALS)) $(MAKEFLAGS)

test:
	go test -race -tags=test ./...

test\:coverage:
	go test -coverprofile=.coverage.out -covermode=atomic -race -tags=test ./...
