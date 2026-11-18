/** Unwraps an openapi-fetch answer: a missing body is an error, so polling keeps the last data and marks the failure. */
export async function ok<T>(request: Promise<{ data?: T; response: { status: number } }>): Promise<T> {
    const result = await request;
    if (result.data === undefined) {
        throw new Error(`HTTP ${result.response.status}`);
    }
    return result.data;
}
