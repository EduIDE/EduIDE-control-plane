package authz

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

const instanceIndex = "status.instance"

// Sessions finds IDESessions by instance id without calling the API server.
type Sessions interface {
	ByInstance(ctx context.Context, instance string) ([]v1alpha1.IDESession, error)
}

// CacheSessions reads from a controller-runtime informer cache.
type CacheSessions struct {
	Cache     cache.Cache
	Namespace string
}

// IndexSessions registers the status.instance field index; call it before starting the cache.
func IndexSessions(ctx context.Context, c cache.Cache) error {
	return c.IndexField(ctx, &v1alpha1.IDESession{}, instanceIndex, func(o client.Object) []string {
		if i := o.(*v1alpha1.IDESession).Status.Instance; i != "" {
			return []string{i}
		}
		return nil
	})
}

func (s CacheSessions) ByInstance(ctx context.Context, instance string) ([]v1alpha1.IDESession, error) {
	var l v1alpha1.IDESessionList
	err := s.Cache.List(ctx, &l, client.InNamespace(s.Namespace),
		client.MatchingFields{instanceIndex: instance}, client.UnsafeDisableDeepCopy)
	return l.Items, err
}
