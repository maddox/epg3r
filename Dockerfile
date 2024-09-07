FROM ruby:latest

RUN apt-get update -qq && apt-get install -y build-essential libpq-dev nodejs

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app
COPY ./parser.rb .
COPY ./run .
CMD ["./run"]
ENTRYPOINT []
