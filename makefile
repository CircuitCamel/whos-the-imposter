run:
	go run ./cmd/imposter

build:
	go build -o ./bin/imposter ./cmd/imposter

clean:
	rm ./bin/imposter && rm -d ./bin/

updateRepo:
	git pull

full: updateRepo build
	./bin/imposter
