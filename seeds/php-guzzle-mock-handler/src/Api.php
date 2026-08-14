<?php

namespace Csx;

use GuzzleHttp\Client;
use GuzzleHttp\Handler\MockHandler;
use GuzzleHttp\HandlerStack;
use GuzzleHttp\Middleware;
use GuzzleHttp\RequestOptions;
use Psr\Http\Message\RequestInterface;
use Psr\Http\Message\ResponseInterface;

/**
 * Testing Guzzle 8 with no network, and the four things that are not what the
 * mock-transport habit or the Guzzle 7 docs lead you to expect.
 *
 * MockHandler is a queue, not a router. It never looks at the request: the
 * next entry comes back whatever method or URL you called, so a test can pass
 * while the code under test hits the wrong endpoint entirely. Proving what was
 * sent is a separate job, and there are two tools for it: getLastRequest() on
 * the MockHandler keeps only the most recent request, while the History
 * middleware keeps every exchange in order — which is why history is worth
 * pushing rather than treated as an optional extra. Running the queue dry is a
 * test-setup error rather than a transport error: MockHandler throws a plain
 * OutOfBoundsException that no GuzzleException catch block will see.
 *
 * Where history sits in the stack changes what it records, and both positions
 * are wrong for something. HandlerStack::create() has already pushed
 * http_errors and prepare_body, and resolve() wraps the stack from the end
 * inwards, so push() lands nearer the handler than anything create() added:
 *   - pushed  (inner): sees the Content-Length prepare_body added, but records
 *     a 500 as a fulfilled response with error => null, because http_errors
 *     converts it to a rejection further out.
 *   - unshifted (outer): records the ServerException in error, but the
 *     request it captured has no Content-Length yet.
 * Content-Type is on the request in both positions. In Guzzle 8 the json
 * option's conditional Content-Type is applied by Client::applyOptions before
 * any middleware runs, so unlike Content-Length it does not depend on
 * prepare_body — which no longer merges conditional headers at all.
 *
 * The handler you pass matters just as much. `new HandlerStack($mock)` is a
 * bare stack with no middleware, which means http_errors is silently inert: a
 * 500 comes back as a response and every error-path test passes for the wrong
 * reason. HandlerStack::create($mock) is what you want.
 *
 * Finally, the Guzzle 7 error-handling idiom is a fatal error on 8.
 * getResponse() and hasResponse() are gone from RequestException; getResponse()
 * now lives on ResponseException and is non-nullable. ServerException still
 * extends RequestException, so `catch (RequestException $e)` still catches a
 * 500 — it is the `$e->getResponse()` inside that catch block that dies.
 * Catch ResponseException (or BadResponseException) when you want the body.
 */
final class Api
{
    /**
     * Filled by the History middleware.
     *
     * @var list<array{request: RequestInterface, response: ?ResponseInterface, error: mixed, options: array}>
     */
    public array $history = [];

    public readonly Client $client;

    public readonly MockHandler $mock;

    /**
     * @param list<mixed> $queue responses, throwables, promises or callables
     * @param bool $recordOutermost unshift history instead of pushing it
     * @param string $baseUri whether the trailing slash is there is not cosmetic
     */
    public function __construct(
        array $queue = [],
        bool $recordOutermost = false,
        string $baseUri = 'https://api.example.com/v1/'
    ) {
        $this->mock = new MockHandler($queue);
        $stack = HandlerStack::create($this->mock);
        if ($recordOutermost) {
            $stack->unshift(Middleware::history($this->history));
        } else {
            $stack->push(Middleware::history($this->history));
        }
        $this->client = new Client([
            'handler' => $stack,
            // The trailing slash in the default is load-bearing. base_uri is
            // merged by RFC 3986 reference resolution, which drops the last
            // segment of a base path that does not end in one.
            'base_uri' => $baseUri,
            'headers' => ['X-Csx' => 'seed'],
        ]);
    }

    /** @return array<string, mixed> */
    public function fetchItems(int $page): array
    {
        $response = $this->client->get('items', [
            RequestOptions::QUERY => ['page' => $page, 'sort' => 'name'],
        ]);

        return json_decode((string) $response->getBody(), true, 512, JSON_THROW_ON_ERROR);
    }

    /** @param array<string, mixed> $payload */
    public function createItem(array $payload): ResponseInterface
    {
        return $this->client->post('items', [
            RequestOptions::JSON => $payload,
            RequestOptions::HEADERS => ['X-Request-Id' => 'r-1'],
        ]);
    }

    /** @param array<string, mixed> $options */
    public function get(string $path, array $options = []): ResponseInterface
    {
        return $this->client->get($path, $options);
    }

    public function lastRequest(): RequestInterface
    {
        return $this->history[count($this->history) - 1]['request'];
    }

    /**
     * The mistake worth being able to spot: a stack built with `new` carries
     * none of the default middleware, so http_errors never runs.
     *
     * @param list<mixed> $queue
     */
    public static function bareStackClient(array $queue): Client
    {
        return new Client([
            'handler' => new HandlerStack(new MockHandler($queue)),
            'base_uri' => 'https://api.example.com/v1/',
        ]);
    }
}
