test:
	go test ./...

test-e2e:
	go test --tags=e2e ./...

tags:
	./tag

install:
	go get

build-docker:
	docker build -t registry.finema.co/finema/mine-core:1.3.0 -t registry.finema.co/finema/mine-core:latest . && docker push registry.finema.co/finema/mine-core:1.3.0 && docker push registry.finema.co/finema/mine-core:latest
