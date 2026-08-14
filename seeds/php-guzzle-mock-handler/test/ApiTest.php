<?php

use Csx\Api;
use GuzzleHttp\Exception\BadResponseException;
use GuzzleHttp\Exception\GuzzleException;
use GuzzleHttp\Exception\RequestException;
use GuzzleHttp\Exception\ResponseException;
use GuzzleHttp\Exception\ServerException;
use GuzzleHttp\Psr7\Request;
use GuzzleHttp\Psr7\Response;
use GuzzleHttp\RequestOptions;
use PHPUnit\Framework\TestCase;

final class ApiTest extends TestCase
{
    public function testTheQueueIsServedInOrderAndTheRealClientStillRuns(): void
    {
        $api = new Api([
            new Response(200, ['Content-Type' => 'application/json'], '{"page":1}'),
            new Response(200, ['Content-Type' => 'application/json'], '{"page":2}'),
        ]);

        $this->assertSame(['page' => 1], $api->fetchItems(1));
        $this->assertSame(['page' => 2], $api->fetchItems(2));

        // Nothing about the client was stubbed out: base_uri resolution, the
        // query option and the default headers all ran for real.
        $this->assertSame(
            'https://api.example.com/v1/items?page=2&sort=name',
            (string) $api->lastRequest()->getUri()
        );
        $this->assertSame('seed', $api->lastRequest()->getHeaderLine('X-Csx'));
        $this->assertCount(0, $api->mock);

        // The MockHandler can answer "what was sent" too, but only for the
        // most recent request. History is what holds both exchanges.
        $this->assertCount(2, $api->history);
        $this->assertSame($api->lastRequest(), $api->mock->getLastRequest());
    }

    public function testTheQueueIsNotARouterAndNeverLooksAtTheRequest(): void
    {
        // The trap. MockHandler pops the next entry whatever you asked for, so
        // a test can pass while the code under test calls the wrong endpoint
        // with the wrong verb. Only the recorded request tells you what was
        // sent; the response proves nothing.
        $api = new Api([new Response(201, [], 'from the queue')]);

        $response = $api->client->request('DELETE', 'nothing/like/items');

        $this->assertSame(201, $response->getStatusCode());
        $this->assertSame('from the queue', (string) $response->getBody());
        $this->assertSame('DELETE', $api->lastRequest()->getMethod());
        $this->assertSame('/v1/nothing/like/items', $api->lastRequest()->getUri()->getPath());
    }

    public function testBaseUriIsMergedByReferenceResolutionNotConcatenation(): void
    {
        $api = new Api([new Response(200), new Response(200)]);

        $api->get('items');
        $this->assertSame('/v1/items', $api->history[0]['request']->getUri()->getPath());

        // Same-looking path, different URL: an absolute-path reference
        // replaces the base path instead of appending to it.
        $api->get('/items');
        $this->assertSame('/items', $api->history[1]['request']->getUri()->getPath());

        // And the trailing slash on base_uri is not decoration. Without it
        // "/v1" is the last segment rather than a directory, so resolution
        // drops it and the request silently leaves the API version behind.
        $unslashed = new Api([new Response(200)], baseUri: 'https://api.example.com/v1');
        $unslashed->get('items');
        $this->assertSame('/items', $unslashed->lastRequest()->getUri()->getPath());
    }

    public function testHttpErrorsTurnsA500IntoAServerExceptionThatKeepsTheBody(): void
    {
        $api = new Api([new Response(500, [], '{"error":"nope"}')]);

        try {
            $api->get('boom');
            $this->fail('a 500 should have thrown');
        } catch (ServerException $exception) {
            // The exception is the response holder, so the body survives the
            // throw and is still decodable — summarising it for the message
            // puts the stream position back where it found it.
            $this->assertSame(500, $exception->getResponse()->getStatusCode());
            $this->assertSame('{"error":"nope"}', $exception->getResponse()->getBody()->getContents());
            $this->assertSame('/v1/boom', $exception->getRequest()->getUri()->getPath());
            $this->assertStringContainsString('500 Internal Server Error', $exception->getMessage());
            // A ResponseException is constructed with the status as its code.
            $this->assertSame(500, $exception->getCode());
        }
    }

    public function testGetResponseIsNoLongerOnRequestExceptionInGuzzle8(): void
    {
        // Measured, and the opposite of every Guzzle 7 snippet: getResponse()
        // and hasResponse() are no longer declared on RequestException, and
        // hasResponse() is gone from the hierarchy outright.
        $this->assertFalse(method_exists(RequestException::class, 'getResponse'));
        $this->assertFalse(method_exists(RequestException::class, 'hasResponse'));
        $this->assertFalse(method_exists(ResponseException::class, 'hasResponse'));
        $this->assertTrue(method_exists(ResponseException::class, 'getResponse'));
        // getResponse() moved down to ResponseException, where the return type
        // is not nullable — a nullable one would stringify with a leading "?"
        // — so the `if ($e->hasResponse())` guard has nothing left to guard.
        $this->assertSame(
            'Psr\Http\Message\ResponseInterface',
            (string) (new ReflectionMethod(ResponseException::class, 'getResponse'))->getReturnType()
        );

        // So the documented Guzzle 7 idiom is a fatal Error on 8, not a
        // deprecation: nothing warns you, the call site just dies.
        $bare = new RequestException('connection reset', new Request('GET', 'https://api.example.com/v1/items'));
        try {
            $bare->getResponse();
            $this->fail('RequestException should no longer have getResponse');
        } catch (Error $error) {
            $this->assertSame(
                'Call to undefined method GuzzleHttp\Exception\RequestException::getResponse()',
                $error->getMessage()
            );
        }

        $api = new Api([new Response(500, [], '{"error":"nope"}')]);
        try {
            $api->get('boom');
            $this->fail('a 500 should have thrown');
        } catch (ResponseException $exception) {
            // ServerException extends BadResponseException extends
            // ResponseException extends RequestException, so an old
            // `catch (RequestException $e)` still fires on a 500. Catching the
            // class that declares getResponse is what makes the body readable
            // again without reflection or a hasResponse() guard.
            $this->assertInstanceOf(ServerException::class, $exception);
            $this->assertInstanceOf(BadResponseException::class, $exception);
            $this->assertInstanceOf(RequestException::class, $exception);
            $this->assertSame('{"error":"nope"}', (string) $exception->getResponse()->getBody());
        }
    }

    public function testHttpErrorsFalseGivesYouTheResponseInstead(): void
    {
        $api = new Api([new Response(500, [], '{"error":"nope"}')]);

        $response = $api->get('boom', [RequestOptions::HTTP_ERRORS => false]);

        $this->assertSame(500, $response->getStatusCode());
        $this->assertSame('{"error":"nope"}', (string) $response->getBody());
    }

    public function testAQueuedThrowableIsThrownExactlyAsQueued(): void
    {
        $queued = new RequestException(
            'connection reset',
            new Request('GET', 'https://api.example.com/v1/items')
        );
        $api = new Api([$queued]);

        try {
            $api->get('items');
            $this->fail('the queued exception should have been thrown');
        } catch (RequestException $exception) {
            // Not a copy and not re-wrapped: the same object comes back out,
            // which is how you test a transport failure with no network.
            $this->assertSame($queued, $exception);
            // A bare RequestException has no response side at all now, so
            // there is nothing to interrogate except the request.
            $this->assertNotInstanceOf(ResponseException::class, $exception);
            $this->assertSame('/v1/items', $exception->getRequest()->getUri()->getPath());
        }
    }

    public function testPushedHistoryRecordsThePreparedRequestButNotTheError(): void
    {
        $api = new Api([new Response(201, [], '{"id":7}')]);

        $api->createItem(['name' => 'widget', 'tags' => ['a']]);
        $request = $api->lastRequest();
        $body = (string) $request->getBody();

        $this->assertSame('POST', $request->getMethod());
        $this->assertSame('{"name":"widget","tags":["a"]}', $body);
        $this->assertSame('application/json', $request->getHeaderLine('Content-Type'));
        // Content-Length is prepare_body's contribution, and a pushed history
        // sits inside it, so this is the finished wire request.
        $this->assertSame((string) strlen($body), $request->getHeaderLine('Content-Length'));
        $this->assertSame('r-1', $request->getHeaderLine('X-Request-Id'));
        $this->assertSame('seed', $request->getHeaderLine('X-Csx'));
        $this->assertSame('GuzzleHttp/8', $request->getHeaderLine('User-Agent'));

        // The cost of that position: http_errors is further out, so the entry
        // for a failed exchange looks like a success.
        $failing = new Api([new Response(500, [], '{"error":"nope"}')]);
        try {
            $failing->get('boom');
        } catch (ServerException) {
        }
        $this->assertNull($failing->history[0]['error']);
        $this->assertSame(500, $failing->history[0]['response']->getStatusCode());
    }

    public function testUnshiftedHistoryRecordsTheErrorButLosesContentLength(): void
    {
        $api = new Api([new Response(201, [], '{"id":7}')], recordOutermost: true);

        $api->createItem(['name' => 'widget', 'tags' => ['a']]);
        $request = $api->lastRequest();

        // Same call, different recorded request, purely because of stack
        // position. Content-Type survives out here because the client applies
        // the json option's conditional header itself; Content-Length does not,
        // so asserting on it fails for a reason unrelated to the code
        // under test.
        $this->assertSame('{"name":"widget","tags":["a"]}', (string) $request->getBody());
        $this->assertSame('application/json', $request->getHeaderLine('Content-Type'));
        $this->assertFalse($request->hasHeader('Content-Length'));

        // What you gain: this side of http_errors, the failure is visible.
        $failing = new Api([new Response(500, [], '{"error":"nope"}')], recordOutermost: true);
        try {
            $failing->get('boom');
        } catch (ServerException) {
        }
        $this->assertInstanceOf(ServerException::class, $failing->history[0]['error']);
        $this->assertNull($failing->history[0]['response']);
    }

    public function testADryQueueRejectsWithAPlainOutOfBoundsException(): void
    {
        $api = new Api([new Response(200)]);
        $api->get('items');

        // Measured, against the assumption that a handler throwing means the
        // call site throws: Client::transfer catches everything the handler
        // raises and returns a rejected promise, so an async call comes back
        // normally and the failure only lands when the promise is waited on.
        $promise = $api->client->getAsync('items');
        $this->assertSame('rejected', $promise->getState());

        try {
            $promise->wait();
            $this->fail('the dry queue should have thrown');
        } catch (OutOfBoundsException $exception) {
            // A plain SPL exception, deliberately outside Guzzle's hierarchy,
            // because an empty queue is a broken test rather than a transport
            // failure — so a catch block for GuzzleException will not see it.
            $this->assertStringContainsString('Mock queue is empty', $exception->getMessage());
            $this->assertNotInstanceOf(GuzzleException::class, $exception);
        }
    }

    public function testABareHandlerStackSilentlyDropsHttpErrors(): void
    {
        // `new HandlerStack($mock)` carries none of the default middleware, so
        // http_errors never runs and every error-path test passes for the
        // wrong reason. HandlerStack::create is what makes the 500 above throw.
        $client = Api::bareStackClient([new Response(500, [], '{"error":"nope"}')]);

        $response = $client->get('boom');

        $this->assertSame(500, $response->getStatusCode());
    }
}
