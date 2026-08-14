run:
	go run main.go

build:
	go build -o ./bin/imposter main.go

clean:
	rm ./bin/imposter && rm -d ./bin/

updateRepo:
	git pull

full: updateRepo build
	./bin/imposter
