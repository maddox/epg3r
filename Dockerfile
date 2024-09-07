FROM mfenniak/ruby-awscli

COPY parser.rb /usr/app/

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app
COPY ./run .
CMD ["./run"]
ENTRYPOINT []
