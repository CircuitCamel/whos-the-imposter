run:
	go run .

build:
	go build -o ./bin/imposter .

clean:
	rm ./bin/imposter && rm -d ./bin/

updateRepo:
	git pull

full: updateRepo build
	./bin/imposter
