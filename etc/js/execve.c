#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <node_api.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

void redirectFileDescriptors()
{
    int fd = open("/dev/tty", O_WRONLY);
    if (fd != STDOUT_FILENO)
    {
        dup2(fd, STDOUT_FILENO);
    }
    if (fd != STDERR_FILENO)
    {
        dup2(fd, STDERR_FILENO);
    }
    close(fd);
}

char *getNullTerminatedString(napi_env env, napi_value jsString)
{
    napi_status status;

    size_t len;
    status = napi_get_value_string_utf8(env, jsString, NULL, 0, &len);
    assert(status == napi_ok);

    size_t len_copied;
    char *str = malloc((len + 1) * sizeof(char));
    napi_get_value_string_utf8(env, jsString, str, len + 1, &len_copied);

    return str;
}

char **jsArrayToCStringArray(napi_env env, napi_value jsArray)
{
    napi_status status;

    uint32_t length;
    status = napi_get_array_length(env, jsArray, &length);
    assert(status == napi_ok);

    char **cArray = malloc((length + 1) * sizeof(char *));
    for (uint32_t i = 0; i < length; i++)
    {
        napi_value element;
        status = napi_get_element(env, jsArray, i, &element);
        assert(status == napi_ok);
        cArray[i] = getNullTerminatedString(env, element);
    }
    cArray[length] = NULL;
    return cArray;
}

napi_value execveWrapper(napi_env env, napi_callback_info info)
{
    napi_status status;

    size_t argc = 3;
    napi_value argv[3];

    status = napi_get_cb_info(env, info, &argc, argv, NULL, NULL);
    assert(status == napi_ok);

    if (argc < 3)
    {
        napi_throw_error(env, NULL, "Three arguments required: path, args[], env[]");
        return NULL;
    }

    char *path = getNullTerminatedString(env, argv[0]);
    char **args = jsArrayToCStringArray(env, argv[1]);
    char **env_vars = jsArrayToCStringArray(env, argv[2]);

    redirectFileDescriptors(); // TODO: This shouldn't be needed
    int result = execve(path, args, env_vars);

    free(path);

    for (char **ptr = args; *ptr != NULL; ptr++)
    {
        free(*ptr);
    }
    free(args);

    for (char **ptr = env_vars; *ptr != NULL; ptr++)
    {
        free(*ptr);
    }
    free(env_vars);

    if (result == -1)
    {
        napi_throw_error(env, strerror(errno), "Error calling execve");
    }

    return NULL;
}

napi_value Init(napi_env env, napi_value exports)
{
    napi_value execve_function;
    napi_create_function(env, NULL, 0, execveWrapper, NULL, &execve_function);
    napi_set_named_property(env, exports, "execve", execve_function);
    return exports;
}

NAPI_MODULE(NODE_GYP_MODULE_NAME, Init)
